package httpapi

import (
	"encoding/base64"
	"html"
	"net/url"
	"regexp"
	"strings"
)

var (
	browserHeadTag   = regexp.MustCompile(`(?i)<head(?:\s[^>]*)?>`)
	browserURLAttr   = regexp.MustCompile(`(?i)\b(src|href|action)=(['"])([^'"]+)['"]`)
	browserCSSURL    = regexp.MustCompile(`(?i)url\(\s*(['"]?)([^'"\)]+)(['"]?)\s*\)`)
	browserCSSImport = regexp.MustCompile(`(?i)(@import\s+)(['"])([^'"]+)(['"])`)
	browserJSImport  = regexp.MustCompile(`(?m)(\bfrom\s*|\bimport\s*(?:\(\s*)?)(['"])([^'"]+)(['"])`)
)

func rewriteBrowserHTML(body []byte, pageURL *url.URL, orgID, sessionID string) []byte {
	if pageURL == nil || pageURL.Host == "" {
		return body
	}
	document := browserURLAttr.ReplaceAllStringFunc(string(body), func(match string) string {
		parts := browserURLAttr.FindStringSubmatch(match)
		if len(parts) != 4 {
			return match
		}
		return parts[1] + "=" + parts[2] + html.EscapeString(browserProxyURL(parts[3], pageURL, orgID, sessionID)) + parts[2]
	})
	prefix := browserProxyBaseURL(pageURL, orgID, sessionID)
	injected := "<base href=\"" + html.EscapeString(prefix) + "\"><script>" +
		browserFetchShim(orgID, sessionID, pageURL.String()) + "</script>"
	if browserHeadTag.MatchString(document) {
		document = browserHeadTag.ReplaceAllStringFunc(document, func(tag string) string {
			return tag + injected
		})
	} else {
		document = injected + document
	}
	return []byte(document)
}

func rewriteBrowserCSS(body []byte, pageURL *url.URL, orgID, sessionID string) []byte {
	stylesheet := browserCSSURL.ReplaceAllStringFunc(string(body), func(match string) string {
		parts := browserCSSURL.FindStringSubmatch(match)
		if len(parts) != 4 {
			return match
		}
		return "url(" + parts[1] + browserProxyURL(parts[2], pageURL, orgID, sessionID) + parts[3] + ")"
	})
	stylesheet = browserCSSImport.ReplaceAllStringFunc(stylesheet, func(match string) string {
		parts := browserCSSImport.FindStringSubmatch(match)
		if len(parts) != 5 {
			return match
		}
		return parts[1] + parts[2] + browserProxyURL(parts[3], pageURL, orgID, sessionID) + parts[4]
	})
	return []byte(stylesheet)
}

func rewriteBrowserJavaScript(body []byte, pageURL *url.URL, orgID, sessionID string) []byte {
	source := browserJSImport.ReplaceAllStringFunc(string(body), func(match string) string {
		parts := browserJSImport.FindStringSubmatch(match)
		if len(parts) != 5 {
			return match
		}
		return parts[1] + parts[2] + browserProxyURL(parts[3], pageURL, orgID, sessionID) + parts[4]
	})
	return []byte(source)
}

func browserProxyBaseURL(pageURL *url.URL, orgID, sessionID string) string {
	base, err := pageURL.Parse(".")
	if err != nil {
		return browserProxyPrefix(orgID, sessionID, pageURL.Scheme+"://"+pageURL.Host)
	}
	return browserProxyURL(base.String(), pageURL, orgID, sessionID)
}

func browserProxyURL(value string, pageURL *url.URL, orgID, sessionID string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(strings.ToLower(trimmed), "data:") || strings.HasPrefix(strings.ToLower(trimmed), "javascript:") {
		return value
	}
	target, err := pageURL.Parse(trimmed)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		return value
	}
	path := strings.TrimPrefix(target.EscapedPath(), "/")
	proxied := browserProxyPrefix(orgID, sessionID, target.Scheme+"://"+target.Host) + path
	if target.RawQuery != "" {
		proxied += "?" + target.RawQuery
	}
	if target.Fragment != "" {
		proxied += "#" + target.Fragment
	}
	return proxied
}

func browserProxyPrefix(orgID, sessionID, origin string) string {
	return "/api/cloud/v1/orgs/" + url.PathEscape(orgID) +
		"/sessions/" + url.PathEscape(sessionID) +
		"/browser/" + base64.RawURLEncoding.EncodeToString([]byte(origin)) + "/"
}

func browserFetchShim(orgID, sessionID, pageURL string) string {
	// This keeps app-origin fetch/XHR calls inside the VM too. It deliberately
	// leaves non-HTTP schemes alone; they are not browser-fetchable resources.
	return `(function(){const page=` + jsString(pageURL) + `,root=` + jsString("/api/cloud/v1/orgs/"+url.PathEscape(orgID)+"/sessions/"+url.PathEscape(sessionID)+"/browser/") + `;const proxy=(value)=>{try{const u=new URL(String(value),page);if(u.protocol!=="http:"&&u.protocol!=="https:")return value;const token=btoa(u.origin).replace(/\+/g,"-").replace(/\//g,"_").replace(/=+$/,"");return root+token+u.pathname.replace(/^\//,"")+u.search+u.hash}catch{return value}};const fetch=window.fetch.bind(window);window.fetch=(input,init)=>{if(typeof input==="string"||input instanceof URL)return fetch(proxy(input),init);if(input instanceof Request)return fetch(new Request(proxy(input.url),input),init);return fetch(input,init)};const open=XMLHttpRequest.prototype.open;XMLHttpRequest.prototype.open=function(method,url){const args=Array.prototype.slice.call(arguments,2);return open.call(this,method,proxy(url),...args)}})();`
}

func jsString(value string) string {
	quoted := strings.ReplaceAll(value, "\\", "\\\\")
	quoted = strings.ReplaceAll(quoted, "`", "\\`")
	quoted = strings.ReplaceAll(quoted, "${", "\\${")
	return "`" + quoted + "`"
}
