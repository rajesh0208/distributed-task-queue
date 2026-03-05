// File: internal/security/validator.go
package security

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var (
	allowedDomains = []string{
		"picsum.photos",
		"unsplash.com",
		"images.unsplash.com",
		"s3.amazonaws.com",
		"storage.googleapis.com",
		"cloudinary.com",
	}

	allowedProtocols    = []string{"https"}
	blockedExtensions   = []string{".exe", ".bat", ".sh", ".php", ".asp"}
	imageExtensionRegex = regexp.MustCompile(`\.(jpg|jpeg|png|gif|webp|bmp|svg)$`)
)

type URLValidator struct {
	allowedDomains   []string
	allowedProtocols []string
	requireHTTPS     bool
	maxURLLength     int
}

func NewURLValidator(requireHTTPS bool) *URLValidator {
	return &URLValidator{
		allowedDomains:   allowedDomains,
		allowedProtocols: allowedProtocols,
		requireHTTPS:     requireHTTPS,
		maxURLLength:     2048,
	}
}

func (v *URLValidator) ValidateImageURL(rawURL string) error {
	if len(rawURL) > v.maxURLLength {
		return fmt.Errorf("URL too long (max %d characters)", v.maxURLLength)
	}

	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL format: %w", err)
	}

	// Allow localhost/uploads and localhost/images URLs (uploaded files and processed images) - skip strict validation
	hostname := strings.ToLower(parsedURL.Hostname())
	if (hostname == "localhost" || hostname == "127.0.0.1" || hostname == "api") &&
		(strings.HasPrefix(parsedURL.Path, "/uploads/") || strings.HasPrefix(parsedURL.Path, "/images/")) {
		// Validate it's an image file
		if !imageExtensionRegex.MatchString(strings.ToLower(parsedURL.Path)) {
			return fmt.Errorf("URL must point to an image file")
		}
		return nil // Allow localhost/api uploads and images (HTTP is OK for internal Docker network)
	}

	if v.requireHTTPS && parsedURL.Scheme != "https" {
		return fmt.Errorf("only HTTPS URLs are allowed")
	}

	protocolAllowed := false
	for _, protocol := range v.allowedProtocols {
		if parsedURL.Scheme == protocol {
			protocolAllowed = true
			break
		}
	}
	if !protocolAllowed {
		return fmt.Errorf("protocol not allowed: %s", parsedURL.Scheme)
	}

	if len(v.allowedDomains) > 0 {
		domainAllowed := false
		hostname := strings.ToLower(parsedURL.Hostname())

		for _, domain := range v.allowedDomains {
			if hostname == domain || strings.HasSuffix(hostname, "."+domain) {
				domainAllowed = true
				break
			}
		}

		if !domainAllowed {
			return fmt.Errorf("domain not allowed: %s", hostname)
		}
	}

	path := strings.ToLower(parsedURL.Path)
	for _, ext := range blockedExtensions {
		if strings.HasSuffix(path, ext) {
			return fmt.Errorf("file extension not allowed: %s", ext)
		}
	}

	if !imageExtensionRegex.MatchString(path) {
		return fmt.Errorf("URL must point to an image file")
	}

	if strings.Contains(parsedURL.Path, "..") || strings.Contains(parsedURL.Path, "//") {
		return fmt.Errorf("URL contains suspicious patterns")
	}

	// Skip private IP check for localhost/api (already handled above)
	hostnameForPrivateCheck := strings.ToLower(parsedURL.Hostname())
	if hostnameForPrivateCheck != "localhost" && hostnameForPrivateCheck != "127.0.0.1" && hostnameForPrivateCheck != "api" {
		if v.isPrivateIP(parsedURL.Hostname()) {
			return fmt.Errorf("private IP addresses are not allowed")
		}
	}

	return nil
}

func (v *URLValidator) isPrivateIP(hostname string) bool {
	privateRanges := []string{
		"127.", "10.",
		"172.16.", "172.17.", "172.18.", "172.19.",
		"172.20.", "172.21.", "172.22.", "172.23.",
		"172.24.", "172.25.", "172.26.", "172.27.",
		"172.28.", "172.29.", "172.30.", "172.31.",
		"192.168.", "localhost", "0.0.0.0",
	}

	hostname = strings.ToLower(hostname)
	for _, prefix := range privateRanges {
		if strings.HasPrefix(hostname, prefix) || hostname == prefix {
			return true
		}
	}
	return false
}

func ValidateTaskPayload(taskType string, payload map[string]interface{}) error {
	validator := NewURLValidator(true)

	requireSourceURL := func() error {
		sourceURL, ok := payload["source_url"].(string)
		if !ok || sourceURL == "" {
			return fmt.Errorf("source_url is required")
		}
		if err := validator.ValidateImageURL(sourceURL); err != nil {
			return fmt.Errorf("invalid source_url: %w", err)
		}
		return nil
	}

	switch taskType {
	case "image_resize", "image_compress", "image_filter", "image_thumbnail",
		"image_format_convert", "image_crop", "image_responsive":
		return requireSourceURL()

	case "image_watermark":
		if err := requireSourceURL(); err != nil {
			return err
		}
		watermarkURL, ok := payload["watermark_url"].(string)
		if !ok || watermarkURL == "" {
			return fmt.Errorf("watermark_url is required")
		}
		if err := validator.ValidateImageURL(watermarkURL); err != nil {
			return fmt.Errorf("invalid watermark_url: %w", err)
		}
	}

	return nil
}
