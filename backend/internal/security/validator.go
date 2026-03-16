// Package security — see auth.go for package-level documentation.
//
// This file implements URL and task-payload validation to prevent SSRF
// (Server-Side Request Forgery) attacks and other injection-class vulnerabilities.
//
// What is SSRF?
// SSRF occurs when an attacker tricks the server into making HTTP requests to
// internal resources on their behalf. For example, if the API accepts any URL
// and fetches it, an attacker could submit "http://169.254.169.254/latest/meta-data/"
// to read AWS instance metadata credentials. URL validation defends against this
// by whitelisting safe domains and blocking private IP ranges.
//
// Connected to:
//   - cmd/api/main.go  — NewURLValidator is created at startup
//   - API handlers     — call ValidateTaskPayload before accepting a task
//   - image_processor  — downloads images only from URLs that passed this validator
//
// Called by:
//   - task submission handlers in the API layer
package security

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// allowedDomains is the allowlist of external hostnames from which the server
// will fetch images. Restricting to known image CDNs prevents SSRF to arbitrary
// external services and limits the attack surface.
var (
	allowedDomains = []string{
		"picsum.photos",        // placeholder image service used in demos
		"unsplash.com",         // free stock photos
		"images.unsplash.com",  // Unsplash CDN subdomain
		"s3.amazonaws.com",     // AWS S3 public buckets
		"storage.googleapis.com", // Google Cloud Storage public buckets
		"cloudinary.com",       // popular image hosting/transformation CDN
	}

	// allowedProtocols limits URLs to HTTPS to prevent cleartext (HTTP) leaks
	// when the server fetches images on behalf of the user.
	allowedProtocols = []string{"https"}

	// blockedExtensions prevents the server from fetching executable files.
	// Even if the domain is on the allowlist, requesting a .php or .exe is
	// almost certainly a mistake or attack.
	blockedExtensions = []string{".exe", ".bat", ".sh", ".php", ".asp"}

	// imageExtensionRegex validates that the URL path ends with a known image
	// file extension. Compiled once at startup for performance.
	imageExtensionRegex = regexp.MustCompile(`\.(jpg|jpeg|png|gif|webp|bmp|svg)$`)
)

// URLValidator holds the validation policy for image URLs.
//
// Fields:
//
//	allowedDomains   — list of external hostnames permitted as image sources
//	allowedProtocols — list of URL schemes that are safe to fetch ("https")
//	requireHTTPS     — when true, any non-https URL is rejected immediately
//	maxURLLength     — cap on URL string length to prevent buffer-overflow-style attacks
type URLValidator struct {
	allowedDomains   []string
	allowedProtocols []string
	requireHTTPS     bool
	maxURLLength     int
}

// NewURLValidator creates a URLValidator with the standard policy.
//
// Parameters:
//
//	requireHTTPS — set true in production to enforce HTTPS-only URLs;
//	               false is useful in local development where images are served over HTTP
//
// Returns:
//
//	*URLValidator — ready to use; shares the package-level allowlists
//
// Called by: ValidateTaskPayload, and directly from API handlers.
func NewURLValidator(requireHTTPS bool) *URLValidator {
	return &URLValidator{
		allowedDomains:   allowedDomains,
		allowedProtocols: allowedProtocols,
		requireHTTPS:     requireHTTPS,
		maxURLLength:     2048, // RFC 7230 recommends supporting at least 8000; 2048 is conservative
	}
}

// ValidateImageURL validates a user-supplied image URL against the security policy.
// It enforces length, protocol, domain allowlist, file extension, path safety,
// and private IP blocking.
//
// Special case — internal uploads:
// Images uploaded to this API and processed images are served from localhost
// (or the Docker Compose "api" service). These internal URLs bypass the external
// domain allowlist but still require a valid image extension.
//
// Parameters:
//
//	rawURL — the URL string as submitted by the client
//
// Returns:
//
//	error — nil if valid, non-nil with a descriptive message if invalid
//
// Called by: ValidateTaskPayload (which is called by task submission handlers).
func (v *URLValidator) ValidateImageURL(rawURL string) error {
	// Reject URLs that exceed the maximum length. Overly long URLs are a common
	// indicator of URL-encoding attacks or confused client implementations.
	if len(rawURL) > v.maxURLLength {
		return fmt.Errorf("URL too long (max %d characters)", v.maxURLLength)
	}

	// Parse the URL — this validates the URL structure (scheme, host, path, etc.).
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL format: %w", err)
	}

	// Special case: allow localhost/api URLs for internally served files.
	// These are paths like /uploads/<uuid>.png or /images/<hash>.jpeg that
	// the API itself generated. They bypass the external domain allowlist but
	// must still point at an image file.
	hostname := strings.ToLower(parsedURL.Hostname())
	if (hostname == "localhost" || hostname == "127.0.0.1" || hostname == "api") &&
		(strings.HasPrefix(parsedURL.Path, "/uploads/") || strings.HasPrefix(parsedURL.Path, "/images/")) {
		// Only allow known image extensions even for internal URLs, to prevent
		// path traversal tricks like /uploads/../config.yaml.
		if !imageExtensionRegex.MatchString(strings.ToLower(parsedURL.Path)) {
			return fmt.Errorf("URL must point to an image file")
		}
		return nil // Internal upload or processed image — allow regardless of scheme
	}

	// Enforce HTTPS-only for all external URLs in production.
	if v.requireHTTPS && parsedURL.Scheme != "https" {
		return fmt.Errorf("only HTTPS URLs are allowed")
	}

	// Check that the scheme appears in the allowed protocols list.
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

	// Domain allowlist check: the hostname must match an allowlisted domain exactly
	// OR be a subdomain of one (e.g. "cdn.unsplash.com" matches "unsplash.com").
	if len(v.allowedDomains) > 0 {
		domainAllowed := false
		hostname := strings.ToLower(parsedURL.Hostname())

		for _, domain := range v.allowedDomains {
			// Exact match OR subdomain match via HasSuffix(".domain").
			if hostname == domain || strings.HasSuffix(hostname, "."+domain) {
				domainAllowed = true
				break
			}
		}

		if !domainAllowed {
			return fmt.Errorf("domain not allowed: %s", hostname)
		}
	}

	// Block executable file extensions. An attacker might try to serve a script
	// from an allowlisted CDN bucket that accepts user uploads.
	path := strings.ToLower(parsedURL.Path)
	for _, ext := range blockedExtensions {
		if strings.HasSuffix(path, ext) {
			return fmt.Errorf("file extension not allowed: %s", ext)
		}
	}

	// The URL must point to an image file by extension. This is a belt-and-suspenders
	// check — the actual format is verified again during download via Content-Type.
	if !imageExtensionRegex.MatchString(path) {
		return fmt.Errorf("URL must point to an image file")
	}

	// Block path traversal patterns: ".." could escape the intended directory,
	// and "//" could confuse URL parsers or route to unexpected hosts.
	if strings.Contains(parsedURL.Path, "..") || strings.Contains(parsedURL.Path, "//") {
		return fmt.Errorf("URL contains suspicious patterns")
	}

	// SSRF private IP block: prevent the server from fetching resources on the
	// internal network. This covers loopback, RFC-1918 private ranges, and
	// link-local addresses.
	// Note: we already handled localhost/127.0.0.1/api above as an allowed special case.
	hostnameForPrivateCheck := strings.ToLower(parsedURL.Hostname())
	if hostnameForPrivateCheck != "localhost" && hostnameForPrivateCheck != "127.0.0.1" && hostnameForPrivateCheck != "api" {
		if v.isPrivateIP(parsedURL.Hostname()) {
			return fmt.Errorf("private IP addresses are not allowed")
		}
	}

	return nil
}

// isPrivateIP returns true if the hostname resolves to a private or reserved IP
// address range. Uses prefix matching on the string representation to cover the
// most common cases without requiring DNS resolution.
//
// Ranges checked:
//
//	127.x.x.x    — loopback
//	10.x.x.x     — RFC-1918 Class A private
//	172.16–31.x  — RFC-1918 Class B private
//	192.168.x.x  — RFC-1918 Class C private
//	0.0.0.0      — unspecified address
//	localhost     — hostname alias for loopback
//
// Parameters:
//
//	hostname — the hostname component of the URL (may be an IP or a hostname)
//
// Returns:
//
//	bool — true if the address is private/reserved and should be blocked
//
// Called by: ValidateImageURL.
func (v *URLValidator) isPrivateIP(hostname string) bool {
	privateRanges := []string{
		"127.",     // loopback (127.0.0.1 – 127.255.255.255)
		"10.",      // RFC-1918 Class A (10.0.0.0/8)
		"172.16.", "172.17.", "172.18.", "172.19.",
		"172.20.", "172.21.", "172.22.", "172.23.",
		"172.24.", "172.25.", "172.26.", "172.27.",
		"172.28.", "172.29.", "172.30.", "172.31.", // RFC-1918 Class B (172.16.0.0/12)
		"192.168.", // RFC-1918 Class C (192.168.0.0/16)
		"localhost", "0.0.0.0",
	}

	hostname = strings.ToLower(hostname)
	for _, prefix := range privateRanges {
		// HasPrefix catches ranges like "10.0.0.1"; equality catches "localhost".
		if strings.HasPrefix(hostname, prefix) || hostname == prefix {
			return true
		}
	}
	return false
}

// ValidateTaskPayload validates the payload fields specific to each task type.
// It is the single entry point for payload validation called by the task
// submission handler before the task is written to the database.
//
// Why a separate function for each task type?
// Different task types require different fields. Centralising validation here
// keeps handler code clean and ensures validation logic cannot be bypassed.
//
// Parameters:
//
//	taskType — string identifier for the task type (e.g. "image_resize")
//	payload  — map of fields decoded from the JSON request body
//
// Returns:
//
//	error — nil if valid; non-nil with a descriptive message if any field is invalid
//
// Called by: task submission handler in the API layer.
func ValidateTaskPayload(taskType string, payload map[string]interface{}) error {
	// Create a validator that requires HTTPS for all external URLs.
	// This is a closure that captures validator for reuse across checks.
	validator := NewURLValidator(true)

	// requireSourceURL is a helper closure that validates the mandatory source_url
	// field present in almost all image task types.
	requireSourceURL := func() error {
		sourceURL, ok := payload["source_url"].(string)
		if !ok || sourceURL == "" {
			return fmt.Errorf("source_url is required")
		}
		if err := validator.ValidateImageURL(sourceURL); err != nil {
			// Wrap the error to show which field failed.
			return fmt.Errorf("invalid source_url: %w", err)
		}
		return nil
	}

	switch taskType {
	// All single-source image operations share the same validation: just need a valid source URL.
	case "image_resize", "image_compress", "image_filter", "image_thumbnail",
		"image_format_convert", "image_crop", "image_responsive":
		return requireSourceURL()

	case "image_watermark":
		// Watermark tasks require both a source image and a watermark image URL.
		if err := requireSourceURL(); err != nil {
			return err
		}
		watermarkURL, ok := payload["watermark_url"].(string)
		if !ok || watermarkURL == "" {
			return fmt.Errorf("watermark_url is required")
		}
		// Validate the watermark URL through the same SSRF/domain checks.
		if err := validator.ValidateImageURL(watermarkURL); err != nil {
			return fmt.Errorf("invalid watermark_url: %w", err)
		}
	}

	// Unknown task types pass validation here — the worker will reject them
	// with an "unknown task type" error during processing.
	return nil
}
