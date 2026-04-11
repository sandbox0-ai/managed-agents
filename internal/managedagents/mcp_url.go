package managedagents

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// CanonicalMCPServerURL normalizes an MCP server URL for matching reusable agent
// definitions to vault credentials. Callers should keep using the original URL
// for network connections.
func CanonicalMCPServerURL(raw string) (string, error) {
	parsedURL, err := parseMCPServerURL(raw)
	if err != nil {
		return "", err
	}
	if parsedURL.Path == "/" {
		parsedURL.Path = ""
	} else {
		parsedURL.Path = strings.TrimRight(parsedURL.Path, "/")
	}
	parsedURL.RawPath = ""
	query := parsedURL.Query()
	parsedURL.RawQuery = query.Encode()
	return strings.TrimRight(parsedURL.String(), "/"), nil
}

// MCPServerURLHost returns the normalized host portion used by sandbox0 network
// policies for an MCP server URL.
func MCPServerURLHost(raw string) (string, error) {
	parsedURL, err := parseMCPServerURL(raw)
	if err != nil {
		return "", err
	}
	return strings.ToLower(strings.TrimSpace(parsedURL.Hostname())), nil
}

func parseMCPServerURL(raw string) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("invalid mcp server url %q", raw)
	}
	parsedURL, err := url.Parse(trimmed)
	if err != nil || strings.TrimSpace(parsedURL.Hostname()) == "" {
		return nil, fmt.Errorf("invalid mcp server url %q", raw)
	}
	scheme := strings.ToLower(strings.TrimSpace(parsedURL.Scheme))
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("mcp server url %q must use http or https", raw)
	}
	host := strings.ToLower(strings.TrimSpace(parsedURL.Hostname()))
	port := strings.TrimSpace(parsedURL.Port())
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	parsedURL.Scheme = scheme
	if port == "" {
		parsedURL.Host = host
	} else {
		parsedURL.Host = net.JoinHostPort(host, port)
	}
	parsedURL.User = nil
	parsedURL.Fragment = ""
	parsedURL.RawFragment = ""
	return parsedURL, nil
}
