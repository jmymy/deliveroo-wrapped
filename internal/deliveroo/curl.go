package deliveroo

import (
	"regexp"
	"strings"
)

// ParsedCurl holds the headers and token extracted from a pasted cURL command.
type ParsedCurl struct {
	URL     string
	Token   string            // full Authorization header value, e.g. "Bearer ..."
	Headers map[string]string // all other captured headers, verbatim
}

var (
	// matches: -H 'Key: Value'  or  -H "Key: Value"  or  --header 'Key: Value'
	curlHeaderRe = regexp.MustCompile(`(?:-H|--header)\s+(?:'([^']*)'|"([^"]*)")`)
	// matches the first quoted/unquoted URL after `curl`
	curlURLRe = regexp.MustCompile(`curl\s+(?:'([^']*)'|"([^"]*)"|(\S+))`)
)

// ParseCurl extracts headers + bearer token from a "Copy as cURL" command, or
// from a raw newline-separated "Key: Value" header block. The Authorization
// header (if present) is pulled out into Token so it can be refreshed
// independently of the rest of the fingerprint.
func ParseCurl(input string) ParsedCurl {
	out := ParsedCurl{Headers: map[string]string{}}

	if m := curlURLRe.FindStringSubmatch(input); m != nil {
		out.URL = firstNonEmpty(m[1], m[2], m[3])
	}

	matches := curlHeaderRe.FindAllStringSubmatch(input, -1)
	if len(matches) > 0 {
		for _, m := range matches {
			addHeader(&out, firstNonEmpty(m[1], m[2]))
		}
		return out
	}

	// Fallback: treat input as a plain "Key: Value" block (one per line).
	for _, line := range strings.Split(input, "\n") {
		addHeader(&out, strings.TrimSpace(line))
	}
	return out
}

func addHeader(out *ParsedCurl, raw string) {
	if raw == "" {
		return
	}
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 {
		return
	}
	key := strings.TrimSpace(parts[0])
	val := strings.TrimSpace(parts[1])
	if key == "" {
		return
	}
	if strings.EqualFold(key, "Authorization") {
		out.Token = val
		return
	}
	out.Headers[key] = val
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
