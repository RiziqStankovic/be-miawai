package research

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Config struct {
	Enabled         bool
	SearxngURL      string
	Timeout         time.Duration
	MaxResults      int
	TargetPages     int
	MaxContentChars int
}

type Client struct {
	cfg  Config
	http *http.Client
}

type SearchResult struct {
	Title   string  `json:"title"`
	URL     string  `json:"url"`
	Snippet string  `json:"snippet"`
	Engine  string  `json:"engine,omitempty"`
	Score   float64 `json:"score,omitempty"`
}

type Page struct {
	SourceURL   string `json:"sourceUrl"`
	FinalURL    string `json:"finalUrl"`
	Title       string `json:"title"`
	TextPreview string `json:"textPreview"`
	Status      int    `json:"status,omitempty"`
	Warning     string `json:"warning,omitempty"`
}

type Report struct {
	Mode        string         `json:"mode"`
	Query       string         `json:"query,omitempty"`
	URL         string         `json:"url,omitempty"`
	Results     []SearchResult `json:"results,omitempty"`
	Pages       []Page         `json:"pages,omitempty"`
	Warnings    []string       `json:"warnings,omitempty"`
	ResultCount int            `json:"resultCount,omitempty"`
	TotalMs     int64          `json:"totalMs"`
	SearxngURL  string         `json:"searxngUrl,omitempty"`
}

func NewClient(cfg Config) *Client {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 12 * time.Second
	}
	if cfg.MaxResults <= 0 || cfg.MaxResults > 10 {
		cfg.MaxResults = 5
	}
	if cfg.TargetPages <= 0 || cfg.TargetPages > 5 {
		cfg.TargetPages = 2
	}
	if cfg.MaxContentChars <= 0 || cfg.MaxContentChars > 20000 {
		cfg.MaxContentChars = 6000
	}
	return &Client{
		cfg: cfg,
		http: &http.Client{
			Timeout: cfg.Timeout,
		},
	}
}

func (c *Client) SearchAndRead(ctx context.Context, query string) (Report, error) {
	startedAt := time.Now()
	query = strings.TrimSpace(query)
	if query == "" {
		return Report{}, errors.New("query is required")
	}
	if !c.cfg.Enabled {
		return Report{}, errors.New("web research is disabled")
	}

	results, err := c.Search(ctx, query)
	if err != nil {
		return Report{}, err
	}

	report := Report{
		Mode:        "search",
		Query:       query,
		Results:     results,
		ResultCount: len(results),
		SearxngURL:  c.cfg.SearxngURL,
	}

	log.Printf("[tool] SearXNG menemukan %d hasil", len(results))
	for index, result := range results {
		log.Printf("[searxng:%d] %s\n  %s\n  %s", index+1, result.Title, result.URL, result.Snippet)
		if len(report.Pages) >= c.cfg.TargetPages {
			break
		}
		if shouldSkipContentFetch(result.URL) {
			report.Warnings = append(report.Warnings, "skipped low-signal source: "+result.URL)
			continue
		}
		pageStartedAt := time.Now()
		page, err := c.ReadURL(ctx, result.URL)
		if err != nil {
			log.Printf("[content-attempt:%d] failed %dms url=%s error=%q", index+1, time.Since(pageStartedAt).Milliseconds(), result.URL, err.Error())
			report.Warnings = append(report.Warnings, fmt.Sprintf("%s: %v", result.URL, err))
			continue
		}
		textLen := len(strings.TrimSpace(page.TextPreview))
		log.Printf("[content-attempt:%d] ok %dms status=%d chars=%d url=%s", index+1, time.Since(pageStartedAt).Milliseconds(), page.Status, textLen, result.URL)
		if textLen < 300 {
			report.Warnings = append(report.Warnings, fmt.Sprintf("short or empty page text (%d chars): %s", textLen, result.URL))
			continue
		}
		if page.Title == "" {
			page.Title = result.Title
		}
		report.Pages = append(report.Pages, page)
	}

	report.TotalMs = time.Since(startedAt).Milliseconds()
	return report, nil
}

func (c *Client) Search(ctx context.Context, query string) ([]SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("query is required")
	}
	if strings.TrimSpace(c.cfg.SearxngURL) == "" {
		return nil, errors.New("SEARXNG_URL is not configured")
	}

	searchURL, err := url.JoinPath(strings.TrimRight(c.cfg.SearxngURL, "/"), "/search")
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(searchURL)
	if err != nil {
		return nil, err
	}
	values := parsed.Query()
	values.Set("q", query)
	values.Set("format", "json")
	values.Set("language", "en")
	parsed.RawQuery = values.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	startedAt := time.Now()
	log.Printf("[searxng] GET %s", parsed.String())
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, err
	}
	log.Printf("[searxng] response %d %dms bytes=%d", resp.StatusCode, time.Since(startedAt).Milliseconds(), len(body))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("SearXNG returned %d: %s", resp.StatusCode, firstLine(string(body)))
	}

	var payload struct {
		Results []struct {
			Title   string  `json:"title"`
			URL     string  `json:"url"`
			Content string  `json:"content"`
			Engine  string  `json:"engine"`
			Score   float64 `json:"score"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}

	results := make([]SearchResult, 0, len(payload.Results))
	seenURLs := make(map[string]struct{}, len(payload.Results))
	for _, item := range payload.Results {
		resultURL := strings.TrimSpace(item.URL)
		if resultURL == "" {
			continue
		}
		canonical := canonicalResultURL(resultURL)
		if _, ok := seenURLs[canonical]; ok {
			continue
		}
		seenURLs[canonical] = struct{}{}
		title := normalizeText(item.Title)
		snippet := normalizeText(item.Content)
		if title == "" && snippet == "" {
			continue
		}
		results = append(results, SearchResult{
			Title:   firstNonEmpty(title, resultURL),
			URL:     resultURL,
			Snippet: snippet,
			Engine:  strings.TrimSpace(item.Engine),
			Score:   item.Score,
		})
	}

	sortResults(results, query)
	if len(results) > c.cfg.MaxResults {
		return results[:c.cfg.MaxResults], nil
	}
	return results, nil
}

func (c *Client) ReadURL(ctx context.Context, rawURL string) (Page, error) {
	parsed, err := validatePublicURL(rawURL)
	if err != nil {
		return Page{SourceURL: rawURL, Warning: err.Error()}, err
	}
	if err := ensurePublicHost(ctx, parsed.Hostname()); err != nil {
		return Page{SourceURL: parsed.String(), Warning: err.Error()}, err
	}

	client := &http.Client{
		Timeout: c.cfg.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if _, err := validatePublicURL(req.URL.String()); err != nil {
				return err
			}
			return ensurePublicHost(req.Context(), req.URL.Hostname())
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return Page{}, err
	}
	req.Header.Set("User-Agent", "MiawAI-WebResearch/0.1")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,application/json,application/xml;q=0.8,*/*;q=0.5")
	req.Header.Set("Accept-Language", "id-ID,id;q=0.9,en-US;q=0.8,en;q=0.7")

	resp, err := client.Do(req)
	if err != nil {
		return Page{SourceURL: parsed.String(), Warning: err.Error()}, err
	}
	defer resp.Body.Close()

	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if !isReadableContentType(contentType) {
		return Page{
			SourceURL: parsed.String(),
			FinalURL:  resp.Request.URL.String(),
			Status:    resp.StatusCode,
			Warning:   "content type is not readable: " + contentType,
		}, nil
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(c.cfg.MaxContentChars*4)))
	if err != nil {
		return Page{}, err
	}
	text := string(raw)
	title := ""
	if strings.Contains(contentType, "html") || looksLikeHTML(text) {
		title = extractTitle(text)
		text = htmlToText(text)
	}
	text = normalizeText(text)
	if len(text) > c.cfg.MaxContentChars {
		text = text[:c.cfg.MaxContentChars]
	}

	page := Page{
		SourceURL:   parsed.String(),
		FinalURL:    resp.Request.URL.String(),
		Title:       title,
		TextPreview: text,
		Status:      resp.StatusCode,
	}
	if resp.StatusCode >= 300 {
		page.Warning = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return page, nil
}

func FormatContext(report Report) string {
	if len(report.Results) == 0 && len(report.Pages) == 0 {
		return ""
	}

	var builder strings.Builder
	builder.WriteString("Web research context. Use this when relevant, cite source URLs, and say if the sources are insufficient.\n")
	if strings.TrimSpace(report.Query) != "" {
		builder.WriteString("Query: ")
		builder.WriteString(report.Query)
		builder.WriteString("\n")
	}
	if len(report.Pages) > 0 {
		builder.WriteString("\nOPENED PAGES WITH READABLE TEXT:\n")
	}
	for index, page := range report.Pages {
		if index >= 3 {
			break
		}
		title := firstNonEmpty(page.Title, page.FinalURL, page.SourceURL)
		builder.WriteString(fmt.Sprintf("[%d] %s\nURL: %s\nText: %s\n", index+1, title, firstNonEmpty(page.FinalURL, page.SourceURL), page.TextPreview))
	}
	if len(report.Results) > 0 {
		builder.WriteString("\nSEARCH RESULT CANDIDATES:\n")
	}
	for index, result := range report.Results {
		if index >= 10 {
			break
		}
		builder.WriteString(fmt.Sprintf("[%d] %s\nURL: %s\nSnippet: %s\n", index+1, result.Title, result.URL, result.Snippet))
	}
	return strings.TrimSpace(builder.String())
}

func validatePublicURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, errors.New("URL is invalid")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("only http/https URLs are allowed")
	}
	if parsed.Hostname() == "" {
		return nil, errors.New("URL host is required")
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return nil, errors.New("localhost URLs are blocked")
	}
	if ip := net.ParseIP(host); ip != nil && !isPublicIP(ip) {
		return nil, errors.New("private or local IP URLs are blocked")
	}
	parsed.Fragment = ""
	return parsed, nil
}

func ensurePublicHost(ctx context.Context, hostname string) error {
	host := strings.ToLower(strings.TrimSuffix(hostname, "."))
	if ip := net.ParseIP(host); ip != nil {
		if !isPublicIP(ip) {
			return errors.New("private or local IP URLs are blocked")
		}
		return nil
	}

	lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(lookupCtx, host)
	if err != nil {
		return nil
	}
	for _, addr := range addrs {
		if !isPublicIP(addr.IP) {
			return errors.New("private or local IP URLs are blocked")
		}
	}
	return nil
}

func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified())
}

func sortResults(results []SearchResult, query string) {
	terms := queryTerms(query)
	sort.SliceStable(results, func(i, j int) bool {
		return scoreResult(results[i], terms) > scoreResult(results[j], terms)
	})
}

func queryTerms(query string) []string {
	parts := regexp.MustCompile(`[^a-zA-Z0-9]+`).Split(strings.ToLower(query), -1)
	terms := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) > 2 {
			terms = append(terms, part)
		}
	}
	return terms
}

func scoreResult(result SearchResult, terms []string) float64 {
	haystack := strings.ToLower(result.Title + " " + result.Snippet + " " + result.URL)
	score := result.Score
	for _, term := range terms {
		if strings.Contains(strings.ToLower(result.Title), term) {
			score += 18
		} else if strings.Contains(haystack, term) {
			score += 10
		}
	}
	if shouldSkipContentFetch(result.URL) {
		score -= 20
	}
	if regexp.MustCompile(`(?i)reuters\.com|apnews\.com|bbc\.com|cnbc\.com|theverge\.com|techcrunch\.com|wired\.com|kompas\.com|detik\.com|tempo\.co|antaranews\.com`).MatchString(result.URL) {
		score += 15
	}
	if regexp.MustCompile(`(?i)/tag/|/category/|/login|/signup|/search\?`).MatchString(result.URL) {
		score -= 8
	}
	return score
}

func shouldSkipContentFetch(rawURL string) bool {
	return regexp.MustCompile(`(?i)youtube\.com|youtu\.be|instagram\.com|facebook\.com|tiktok\.com|x\.com|twitter\.com|linkedin\.com|pinterest\.com|reddit\.com`).MatchString(rawURL)
}

func canonicalResultURL(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return strings.TrimSpace(rawURL)
	}
	parsed.Fragment = ""
	query := parsed.Query()
	for key := range query {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "utm_") || lower == "fbclid" || lower == "gclid" {
			query.Del(key)
		}
	}
	parsed.RawQuery = query.Encode()
	return strings.TrimRight(parsed.String(), "/")
}

func isReadableContentType(contentType string) bool {
	return contentType == "" ||
		strings.Contains(contentType, "text/html") ||
		strings.Contains(contentType, "application/xhtml+xml") ||
		strings.Contains(contentType, "text/plain") ||
		strings.Contains(contentType, "application/json") ||
		strings.Contains(contentType, "application/xml") ||
		strings.Contains(contentType, "text/xml")
}

func looksLikeHTML(text string) bool {
	lower := strings.ToLower(text[:min(len(text), 512)])
	return strings.Contains(lower, "<html") || strings.Contains(lower, "<body") || strings.Contains(lower, "<!doctype html")
}

func extractTitle(rawHTML string) string {
	match := regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`).FindStringSubmatch(rawHTML)
	if len(match) < 2 {
		return ""
	}
	return normalizeText(html.UnescapeString(match[1]))
}

func htmlToText(rawHTML string) string {
	text := regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>`).ReplaceAllString(rawHTML, " ")
	text = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style>`).ReplaceAllString(text, " ")
	text = regexp.MustCompile(`(?is)<noscript\b[^>]*>.*?</noscript>`).ReplaceAllString(text, " ")
	text = regexp.MustCompile(`(?is)<[^>]+>`).ReplaceAllString(text, " ")
	return html.UnescapeString(text)
}

func normalizeText(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func firstLine(text string) string {
	line := strings.TrimSpace(strings.Split(text, "\n")[0])
	if len(line) > 300 {
		return line[:300]
	}
	return line
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
