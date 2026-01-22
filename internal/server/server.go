package server

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/guiyumin/vget/internal/core/config"
	"github.com/guiyumin/vget/internal/core/downloader"
	"github.com/guiyumin/vget/internal/core/extractor"
	"github.com/guiyumin/vget/internal/core/i18n"
	"github.com/guiyumin/vget/internal/core/version"
)

// Response is the standard API response structure
type Response struct {
	Code    int    `json:"code"`
	Data    any    `json:"data"`
	Message string `json:"message"`
}

// DownloadRequest is the request body for POST /download
type DownloadRequest struct {
	URL        string `json:"url" binding:"required"`
	Filename   string `json:"filename,omitempty"`
	ReturnFile bool   `json:"return_file,omitempty"`
}

// BulkDownloadRequest is the request body for POST /bulk-download
type BulkDownloadRequest struct {
	URLs []string `json:"urls" binding:"required"`
}

// Server is the HTTP server for vget
type Server struct {
	port       int
	outputDir  string
	apiKey     string
	jobQueue   *JobQueue
	cfg        *config.Config
	server     *http.Server
	engine     *gin.Engine
}

// NewServer creates a new HTTP server
func NewServer(port int, outputDir, apiKey string, maxConcurrent int) *Server {
	cfg := config.LoadOrDefault()

	s := &Server{
		port:      port,
		outputDir: outputDir,
		apiKey:    apiKey,
		cfg:       cfg,
	}

	// Create job queue with download function
	s.jobQueue = NewJobQueue(maxConcurrent, outputDir, s.downloadWithExtractor)

	return s
}

// Start starts the HTTP server
func (s *Server) Start() error {
	// Warn if no config file exists
	if !config.Exists() {
		lang := s.cfg.Language
		if lang == "" {
			lang = "zh"
		}
		t := i18n.GetTranslations(lang)
		log.Printf("⚠️  %s", t.Server.NoConfigWarning)
		log.Printf("   %s", t.Server.RunInitHint)
	}

	// Ensure output directory exists
	if err := os.MkdirAll(s.outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Start job queue workers
	s.jobQueue.Start()

	// Set Gin mode
	gin.SetMode(gin.ReleaseMode)

	// Create Gin engine
	s.engine = gin.New()

	// Add middleware
	s.engine.Use(gin.Recovery())
	s.engine.Use(s.loggingMiddleware())
	if s.apiKey != "" {
		s.engine.Use(s.jwtAuthMiddleware())
	}

	// API routes
	api := s.engine.Group("/api")
	api.GET("/health", s.handleHealth)

	// Auth routes (don't require authentication)
	api.GET("/auth/status", s.handleAuthStatus)
	api.POST("/auth/token", s.handleGenerateToken)

	api.GET("/download", s.handleFileDownload) // Download local file by path
	api.POST("/download", s.handleDownload)
	api.POST("/bulk-download", s.handleBulkDownload)
	api.GET("/status/:id", s.handleStatus)
	api.GET("/jobs", s.handleGetJobs)
	api.DELETE("/jobs", s.handleClearJobs)
	api.DELETE("/jobs/:id", s.handleDeleteJob)
	api.GET("/config", s.handleGetConfig)
	api.POST("/config", s.handleSetConfig)
	api.PUT("/config", s.handleUpdateConfig)
	api.GET("/i18n", s.handleI18n)

	s.server = &http.Server{
		Addr:         fmt.Sprintf(":%d", s.port),
		Handler:      s.engine,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // No timeout for downloads
		IdleTimeout:  120 * time.Second,
	}

	log.Printf("Starting vget server on port %d", s.port)
	log.Printf("Output directory: %s", s.outputDir)
	if s.apiKey != "" {
		log.Printf("API key authentication enabled")
	}

	return s.server.ListenAndServe()
}

// Stop gracefully shuts down the server
func (s *Server) Stop(ctx context.Context) error {
	s.jobQueue.Stop()
	return s.server.Shutdown(ctx)
}

// Middleware

func (s *Server) loggingMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		log.Printf("%s %s %s", c.Request.Method, c.Request.URL.Path, time.Since(start))
	}
}

// Handlers

func (s *Server) handleHealth(c *gin.Context) {
	c.JSON(http.StatusOK, Response{
		Code: 200,
		Data: gin.H{
			"status":  "ok",
			"version": version.Version,
		},
		Message: "everything is good",
	})
}

// handleFileDownload serves a local file for download
func (s *Server) handleFileDownload(c *gin.Context) {
	filePath := c.Query("path")
	if filePath == "" {
		c.JSON(http.StatusBadRequest, Response{
			Code:    400,
			Data:    nil,
			Message: "path parameter is required",
		})
		return
	}

	// Security: ensure the file is within the output directory
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		c.JSON(http.StatusBadRequest, Response{
			Code:    400,
			Data:    nil,
			Message: "invalid path",
		})
		return
	}

	absOutputDir, _ := filepath.Abs(s.outputDir)
	if !strings.HasPrefix(absPath, absOutputDir) {
		c.JSON(http.StatusForbidden, Response{
			Code:    403,
			Data:    nil,
			Message: "access denied: file outside output directory",
		})
		return
	}

	// Check file exists
	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		c.JSON(http.StatusNotFound, Response{
			Code:    404,
			Data:    nil,
			Message: "file not found",
		})
		return
	}

	// Serve the file
	filename := filepath.Base(absPath)
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	c.File(absPath)
}

func (s *Server) handleDownload(c *gin.Context) {
	var req DownloadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, Response{
			Code:    400,
			Data:    nil,
			Message: "invalid request body: url is required",
		})
		return
	}

	// If return_file is true, download and stream directly
	if req.ReturnFile {
		s.downloadAndStream(c, req.URL, req.Filename)
		return
	}

	// Otherwise, queue the download
	job, err := s.jobQueue.AddJob(req.URL, req.Filename)
	if err != nil {
		c.JSON(http.StatusInternalServerError, Response{
			Code:    500,
			Data:    nil,
			Message: err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, Response{
		Code: 200,
		Data: gin.H{
			"id":     job.ID,
			"status": job.Status,
		},
		Message: "download started",
	})
}

func (s *Server) handleBulkDownload(c *gin.Context) {
	var req BulkDownloadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, Response{
			Code:    400,
			Data:    nil,
			Message: "invalid request body: urls array is required",
		})
		return
	}

	if len(req.URLs) == 0 {
		c.JSON(http.StatusBadRequest, Response{
			Code:    400,
			Data:    nil,
			Message: "urls array cannot be empty",
		})
		return
	}

	// Queue all downloads
	var jobs []gin.H
	var queued, failed int

	for _, url := range req.URLs {
		url = strings.TrimSpace(url)
		// Skip empty lines and comments
		if url == "" || strings.HasPrefix(url, "#") {
			continue
		}

		job, err := s.jobQueue.AddJob(url, "")
		if err != nil {
			// Create a failed job so clients can see it in job listings
			failedJob := s.jobQueue.AddFailedJob(url, err.Error())
			jobs = append(jobs, gin.H{
				"id":     failedJob.ID,
				"url":    failedJob.URL,
				"status": failedJob.Status,
				"error":  failedJob.Error,
			})
			failed++
			continue
		}
		jobs = append(jobs, gin.H{
			"id":     job.ID,
			"url":    job.URL,
			"status": job.Status,
		})
		queued++
	}

	c.JSON(http.StatusOK, Response{
		Code: 200,
		Data: gin.H{
			"jobs":   jobs,
			"queued": queued,
			"failed": failed,
		},
		Message: fmt.Sprintf("%d downloads queued", queued),
	})
}

func (s *Server) handleStatus(c *gin.Context) {
	id := c.Param("id")

	job := s.jobQueue.GetJob(id)
	if job == nil {
		c.JSON(http.StatusNotFound, Response{
			Code:    404,
			Data:    nil,
			Message: "job not found",
		})
		return
	}

	c.JSON(http.StatusOK, Response{
		Code: 200,
		Data: gin.H{
			"id":       job.ID,
			"status":   job.Status,
			"progress": job.Progress,
			"filename": job.Filename,
			"error":    job.Error,
		},
		Message: string(job.Status),
	})
}

func (s *Server) handleGetJobs(c *gin.Context) {
	jobs := s.jobQueue.GetAllJobs()

	jobList := make([]gin.H, len(jobs))
	for i, job := range jobs {
		jobList[i] = gin.H{
			"id":         job.ID,
			"url":        job.URL,
			"status":     job.Status,
			"progress":   job.Progress,
			"downloaded": job.Downloaded,
			"total":      job.Total,
			"filename":   job.Filename,
			"error":      job.Error,
		}
	}

	c.JSON(http.StatusOK, Response{
		Code: 200,
		Data: gin.H{
			"jobs": jobList,
		},
		Message: fmt.Sprintf("%d jobs found", len(jobs)),
	})
}

func (s *Server) handleClearJobs(c *gin.Context) {
	count := s.jobQueue.ClearHistory()
	c.JSON(http.StatusOK, Response{
		Code: 200,
		Data: gin.H{
			"cleared": count,
		},
		Message: fmt.Sprintf("%d jobs cleared", count),
	})
}

func (s *Server) handleDeleteJob(c *gin.Context) {
	id := c.Param("id")

	// Try to cancel active job first, then try to remove finished job
	if s.jobQueue.CancelJob(id) {
		c.JSON(http.StatusOK, Response{
			Code:    200,
			Data:    gin.H{"id": id},
			Message: "job cancelled",
		})
	} else if s.jobQueue.RemoveJob(id) {
		c.JSON(http.StatusOK, Response{
			Code:    200,
			Data:    gin.H{"id": id},
			Message: "job removed",
		})
	} else {
		c.JSON(http.StatusNotFound, Response{
			Code:    404,
			Data:    nil,
			Message: "job not found or cannot be cancelled/removed",
		})
	}
}

// ConfigSetRequest is the request body for POST /config
type ConfigSetRequest struct {
	Key   string `json:"key" binding:"required"`
	Value string `json:"value"`
}

// ConfigRequest is the request body for PUT /config
type ConfigRequest struct {
	OutputDir string `json:"output_dir,omitempty"`
}

func (s *Server) handleGetConfig(c *gin.Context) {
	cfg := config.LoadOrDefault()

	c.JSON(http.StatusOK, Response{
		Code: 200,
		Data: gin.H{
			"output_dir":            s.outputDir,
			"language":              cfg.Language,
			"format":                cfg.Format,
			"quality":               cfg.Quality,
			"twitter_auth_token":    cfg.Twitter.AuthToken,
			"server_port":           cfg.Server.Port,
			"server_max_concurrent": cfg.Server.MaxConcurrent,
			"server_api_key":        cfg.Server.APIKey,
		},
		Message: "config retrieved",
	})
}

func (s *Server) handleSetConfig(c *gin.Context) {
	var req ConfigSetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, Response{
			Code:    400,
			Data:    nil,
			Message: "invalid request body: key is required",
		})
		return
	}

	// Load current config, update, save
	cfg := config.LoadOrDefault()
	if err := s.setConfigValue(cfg, req.Key, req.Value); err != nil {
		c.JSON(http.StatusBadRequest, Response{
			Code:    400,
			Data:    nil,
			Message: err.Error(),
		})
		return
	}

	if err := config.Save(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, Response{
			Code:    500,
			Data:    nil,
			Message: fmt.Sprintf("failed to save config: %v", err),
		})
		return
	}

	// Update server's cached config
	s.cfg = cfg

	// Special handling for output_dir
	if req.Key == "output_dir" {
		if err := os.MkdirAll(req.Value, 0755); err != nil {
			c.JSON(http.StatusBadRequest, Response{
				Code:    400,
				Data:    nil,
				Message: fmt.Sprintf("invalid output directory: %v", err),
			})
			return
		}
		s.outputDir = req.Value
		s.jobQueue.outputDir = req.Value
	}

	c.JSON(http.StatusOK, Response{
		Code: 200,
		Data: gin.H{
			"key":   req.Key,
			"value": req.Value,
		},
		Message: fmt.Sprintf("config %s updated", req.Key),
	})
}

func (s *Server) handleUpdateConfig(c *gin.Context) {
	var req ConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, Response{
			Code:    400,
			Data:    nil,
			Message: "invalid request body",
		})
		return
	}

	if req.OutputDir != "" {
		if err := os.MkdirAll(req.OutputDir, 0755); err != nil {
			c.JSON(http.StatusBadRequest, Response{
				Code:    400,
				Data:    nil,
				Message: fmt.Sprintf("invalid output directory: %v", err),
			})
			return
		}

		s.outputDir = req.OutputDir
		s.jobQueue.outputDir = req.OutputDir

		cfg := config.LoadOrDefault()
		cfg.OutputDir = req.OutputDir
		if err := config.Save(cfg); err != nil {
			log.Printf("Warning: failed to save config: %v", err)
		}
	}

	c.JSON(http.StatusOK, Response{
		Code: 200,
		Data: gin.H{
			"output_dir": s.outputDir,
		},
		Message: "config updated",
	})
}

func (s *Server) handleI18n(c *gin.Context) {
	lang := s.cfg.Language
	if lang == "" {
		lang = "zh"
	}

	t := i18n.GetTranslations(lang)

	c.JSON(http.StatusOK, Response{
		Code: 200,
		Data: gin.H{
			"language":      lang,
			"ui":            t.UI,
			"server":        t.Server,
			"config_exists": config.Exists(),
		},
		Message: "translations retrieved",
	})
}

// Helper functions

// setConfigValue sets a config value by key
func (s *Server) setConfigValue(cfg *config.Config, key, value string) error {
	switch key {
	case "language":
		cfg.Language = value
	case "output_dir":
		cfg.OutputDir = value
	case "format":
		cfg.Format = value
	case "quality":
		cfg.Quality = value
	case "twitter_auth_token", "twitter.auth_token":
		cfg.Twitter.AuthToken = value
	case "server.max_concurrent", "server_max_concurrent":
		var val int
		if _, err := fmt.Sscanf(value, "%d", &val); err != nil {
			return fmt.Errorf("invalid value for max_concurrent: %s", value)
		}
		cfg.Server.MaxConcurrent = val
	case "server.api_key", "server_api_key":
		cfg.Server.APIKey = value
	default:
		return fmt.Errorf("unknown config key: %s", key)
	}
	return nil
}

// downloadWithExtractor is the download function used by the job queue
func (s *Server) downloadWithExtractor(ctx context.Context, url, filename string, progressFn func(downloaded, total int64)) error {
	// Find matching extractor
	ext := extractor.Match(url)
	if ext == nil {
		sitesConfig, _ := config.LoadSites()
		if sitesConfig != nil {
			if site := sitesConfig.MatchSite(url); site != nil {
				ext = extractor.NewBrowserExtractor(site, false)
			}
		}
		if ext == nil {
			ext = extractor.NewGenericBrowserExtractor(false)
		}
	}

	// Configure Twitter extractor with auth if available
	if twitterExt, ok := ext.(*extractor.TwitterExtractor); ok {
		if s.cfg.Twitter.AuthToken != "" {
			twitterExt.SetAuth(s.cfg.Twitter.AuthToken)
		}
	}

	// Extract media info
	media, err := ext.Extract(url)
	if err != nil {
		return fmt.Errorf("extraction failed: %w", err)
	}

	// Determine output path based on media type
	var outputPath string
	var downloadURL string
	var headers map[string]string

	switch m := media.(type) {
	case *extractor.VideoMedia:
		if len(m.Formats) == 0 {
			return fmt.Errorf("no video formats available")
		}
		format := selectBestFormat(m.Formats)
		downloadURL = format.URL
		headers = format.Headers

		ext := format.Ext
		if ext == "m3u8" {
			ext = "ts"
		}

		if filename != "" {
			// Sanitize the provided filename to remove invalid path characters
			sanitized := extractor.SanitizeFilename(filename)
			// Ensure the filename has the correct extension
			if !strings.HasSuffix(strings.ToLower(sanitized), "."+ext) {
				sanitized = fmt.Sprintf("%s.%s", sanitized, ext)
			}
			outputPath = filepath.Join(s.outputDir, sanitized)
		} else {
			title := extractor.SanitizeFilename(m.Title)
			if title != "" {
				outputPath = filepath.Join(s.outputDir, fmt.Sprintf("%s.%s", title, ext))
			} else {
				outputPath = filepath.Join(s.outputDir, fmt.Sprintf("%s.%s", m.ID, ext))
			}
		}

		s.updateJobFilename(url, outputPath)

		// Handle separate audio stream
		if format.AudioURL != "" {
			return s.downloadVideoWithAudio(ctx, format, outputPath, progressFn)
		}

	case *extractor.AudioMedia:
		downloadURL = m.URL

		if filename != "" {
			// Sanitize the provided filename to remove invalid path characters
			sanitized := extractor.SanitizeFilename(filename)
			// Ensure the filename has the correct extension
			if !strings.HasSuffix(strings.ToLower(sanitized), "."+m.Ext) {
				sanitized = fmt.Sprintf("%s.%s", sanitized, m.Ext)
			}
			outputPath = filepath.Join(s.outputDir, sanitized)
		} else {
			title := extractor.SanitizeFilename(m.Title)
			if title != "" {
				outputPath = filepath.Join(s.outputDir, fmt.Sprintf("%s.%s", title, m.Ext))
			} else {
				outputPath = filepath.Join(s.outputDir, fmt.Sprintf("%s.%s", m.ID, m.Ext))
			}
		}

		s.updateJobFilename(url, outputPath)

	case *extractor.ImageMedia:
		if len(m.Images) == 0 {
			return fmt.Errorf("no images available")
		}

		title := extractor.SanitizeFilename(m.Title)
		var filenames []string

		for i, img := range m.Images {
			var imgPath string
			if len(m.Images) == 1 {
				if title != "" {
					imgPath = filepath.Join(s.outputDir, fmt.Sprintf("%s.%s", title, img.Ext))
				} else {
					imgPath = filepath.Join(s.outputDir, fmt.Sprintf("%s.%s", m.ID, img.Ext))
				}
			} else {
				if title != "" {
					imgPath = filepath.Join(s.outputDir, fmt.Sprintf("%s_%d.%s", title, i+1, img.Ext))
				} else {
					imgPath = filepath.Join(s.outputDir, fmt.Sprintf("%s_%d.%s", m.ID, i+1, img.Ext))
				}
			}

			filenames = append(filenames, imgPath)

			if err := downloadFile(ctx, img.URL, imgPath, nil, nil); err != nil {
				return fmt.Errorf("failed to download image %d: %w", i+1, err)
			}
		}

		s.updateJobFilename(url, strings.Join(filenames, ", "))
		return nil

	default:
		return fmt.Errorf("unsupported media type")
	}

	// Check if this is an HLS stream
	if strings.HasSuffix(strings.ToLower(downloadURL), ".m3u8") ||
		strings.Contains(strings.ToLower(downloadURL), ".m3u8?") {
		finalPath, err := downloader.DownloadHLSWithProgress(ctx, downloadURL, outputPath, headers, progressFn)
		if err != nil {
			return err
		}
		if finalPath != outputPath {
			s.updateJobFilename(url, finalPath)
		}
		return nil
	}

	return downloadFile(ctx, downloadURL, outputPath, headers, progressFn)
}

func (s *Server) updateJobFilename(url, filename string) {
	jobs := s.jobQueue.GetAllJobs()
	for _, job := range jobs {
		if job.URL == url {
			s.jobQueue.mu.Lock()
			if j, ok := s.jobQueue.jobs[job.ID]; ok {
				j.Filename = filename
			}
			s.jobQueue.mu.Unlock()
			break
		}
	}
}

// downloadVideoWithAudio downloads video and audio in parallel then merges them with ffmpeg
func (s *Server) downloadVideoWithAudio(ctx context.Context, format *extractor.VideoFormat, outputPath string, progressFn func(downloaded, total int64)) error {
	// Determine audio extension based on video format
	audioExt := "m4a"
	if format.Ext == "webm" {
		audioExt = "opus"
	}

	// Build filenames
	ext := filepath.Ext(outputPath)
	baseName := strings.TrimSuffix(outputPath, ext)
	videoFile := outputPath
	audioFile := baseName + "." + audioExt

	// Track progress from both downloads
	var videoDownloaded, videoTotal int64
	var audioDownloaded, audioTotal int64
	var mu sync.Mutex

	reportProgress := func() {
		if progressFn != nil {
			mu.Lock()
			total := videoTotal + audioTotal
			downloaded := videoDownloaded + audioDownloaded
			mu.Unlock()
			if total > 0 {
				progressFn(downloaded, total)
			}
		}
	}

	// Download video and audio in parallel
	var wg sync.WaitGroup
	var videoErr, audioErr error

	wg.Add(2)

	// Download video stream
	go func() {
		defer wg.Done()
		videoErr = downloadFile(ctx, format.URL, videoFile, format.Headers, func(downloaded, total int64) {
			mu.Lock()
			videoDownloaded = downloaded
			videoTotal = total
			mu.Unlock()
			reportProgress()
		})
	}()

	// Download audio stream
	go func() {
		defer wg.Done()
		audioErr = downloadFile(ctx, format.AudioURL, audioFile, format.Headers, func(downloaded, total int64) {
			mu.Lock()
			audioDownloaded = downloaded
			audioTotal = total
			mu.Unlock()
			reportProgress()
		})
	}()

	wg.Wait()

	// Check for errors
	if videoErr != nil {
		return fmt.Errorf("failed to download video stream: %w", videoErr)
	}
	if audioErr != nil {
		return fmt.Errorf("failed to download audio stream: %w", audioErr)
	}

	// Try to merge with ffmpeg if available
	if downloader.FFmpegAvailable() {
		_, err := downloader.MergeVideoAudioKeepOriginals(videoFile, audioFile)
		if err != nil {
			// Merge failed but downloads succeeded - log warning but don't fail
			log.Printf("Warning: ffmpeg merge failed: %v (files kept: %s, %s)", err, videoFile, audioFile)
		}
	} else {
		// ffmpeg not available - just leave the separate files
		log.Printf("ffmpeg not found, video and audio saved separately: %s, %s", videoFile, audioFile)
	}

	return nil
}

// downloadAndStream extracts and streams the file directly to the response
func (s *Server) downloadAndStream(c *gin.Context, url, filename string) {
	ext := extractor.Match(url)
	if ext == nil {
		sitesConfig, _ := config.LoadSites()
		if sitesConfig != nil {
			if site := sitesConfig.MatchSite(url); site != nil {
				ext = extractor.NewBrowserExtractor(site, false)
			}
		}
		if ext == nil {
			ext = extractor.NewGenericBrowserExtractor(false)
		}
	}

	if twitterExt, ok := ext.(*extractor.TwitterExtractor); ok {
		if s.cfg.Twitter.AuthToken != "" {
			twitterExt.SetAuth(s.cfg.Twitter.AuthToken)
		}
	}

	media, err := ext.Extract(url)
	if err != nil {
		c.JSON(http.StatusInternalServerError, Response{
			Code:    500,
			Data:    nil,
			Message: fmt.Sprintf("extraction failed: %v", err),
		})
		return
	}

	var downloadURL string
	var headers map[string]string
	var outputFilename string

	switch m := media.(type) {
	case *extractor.VideoMedia:
		if len(m.Formats) == 0 {
			c.JSON(http.StatusInternalServerError, Response{
				Code:    500,
				Data:    nil,
				Message: "no video formats available",
			})
			return
		}
		format := selectBestFormat(m.Formats)
		downloadURL = format.URL
		headers = format.Headers

		if filename != "" {
			outputFilename = filename
		} else {
			title := extractor.SanitizeFilename(m.Title)
			ext := format.Ext
			if ext == "m3u8" {
				ext = "ts"
			}
			if title != "" {
				outputFilename = fmt.Sprintf("%s.%s", title, ext)
			} else {
				outputFilename = fmt.Sprintf("%s.%s", m.ID, ext)
			}
		}

	case *extractor.AudioMedia:
		downloadURL = m.URL
		if filename != "" {
			outputFilename = filename
		} else {
			title := extractor.SanitizeFilename(m.Title)
			if title != "" {
				outputFilename = fmt.Sprintf("%s.%s", title, m.Ext)
			} else {
				outputFilename = fmt.Sprintf("%s.%s", m.ID, m.Ext)
			}
		}

	case *extractor.ImageMedia:
		if len(m.Images) == 0 {
			c.JSON(http.StatusInternalServerError, Response{
				Code:    500,
				Data:    nil,
				Message: "no images available",
			})
			return
		}
		img := m.Images[0]
		downloadURL = img.URL
		if filename != "" {
			outputFilename = filename
		} else {
			title := extractor.SanitizeFilename(m.Title)
			if title != "" {
				outputFilename = fmt.Sprintf("%s.%s", title, img.Ext)
			} else {
				outputFilename = fmt.Sprintf("%s.%s", m.ID, img.Ext)
			}
		}

	default:
		c.JSON(http.StatusInternalServerError, Response{
			Code:    500,
			Data:    nil,
			Message: "unsupported media type",
		})
		return
	}

	streamFile(c.Writer, downloadURL, outputFilename, headers)
}

func selectBestFormat(formats []extractor.VideoFormat) *extractor.VideoFormat {
	if len(formats) == 0 {
		return nil
	}

	var bestWithAudio *extractor.VideoFormat
	for i := range formats {
		f := &formats[i]
		if f.AudioURL != "" {
			if bestWithAudio == nil || f.Bitrate > bestWithAudio.Bitrate {
				bestWithAudio = f
			}
		}
	}
	if bestWithAudio != nil {
		return bestWithAudio
	}

	best := &formats[0]
	for i := range formats {
		if formats[i].Bitrate > best.Bitrate {
			best = &formats[i]
		}
	}
	return best
}

func downloadFile(ctx context.Context, url, outputPath string, headers map[string]string, progressFn func(downloaded, total int64)) error {
	client := &http.Client{
		Timeout: 0,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
		},
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	if len(headers) > 0 {
		for key, value := range headers {
			req.Header.Set(key, value)
		}
	} else {
		req.Header.Set("User-Agent", downloader.DefaultUserAgent)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	total := resp.ContentLength

	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer file.Close()

	buf := make([]byte, 32*1024)
	var downloaded int64

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			_, writeErr := file.Write(buf[:n])
			if writeErr != nil {
				return fmt.Errorf("failed to write file: %w", writeErr)
			}
			downloaded += int64(n)
			if progressFn != nil {
				progressFn(downloaded, total)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("download failed: %w", readErr)
		}
	}

	return nil
}

func streamFile(w http.ResponseWriter, url, filename string, headers map[string]string) {
	client := &http.Client{
		Timeout: 0,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
		},
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		http.Error(w, "failed to create request", http.StatusInternalServerError)
		return
	}

	if len(headers) > 0 {
		for key, value := range headers {
			req.Header.Set(key, value)
		}
	} else {
		req.Header.Set("User-Agent", downloader.DefaultUserAgent)
	}

	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "download request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		http.Error(w, fmt.Sprintf("upstream returned status %d", resp.StatusCode), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	if resp.ContentLength > 0 {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", resp.ContentLength))
	}
	if contentType := resp.Header.Get("Content-Type"); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}

	io.Copy(w, resp.Body)
}
