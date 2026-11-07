package routes

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"meta-lattice/src/models"
	"meta-lattice/src/storage"
)

type RouteEndpoint struct {
	ID          string `json:"id"`
	Method      string `json:"method"`
	Path        string `json:"path"`
	HandlerName string `json:"handler_name"`
	HandlerID   string `json:"handler_id,omitempty"`
	FilePath    string `json:"file_path"`
	Line        int    `json:"line"`
	Framework   string `json:"framework"`
}

var (
	// Go: r.GET("/path", handler), e.POST("/path", handler), etc.
	goRouteRegex = regexp.MustCompile(`(?m)\b(?:r|router|app|e|api|group|v1|v2|echo)\.(GET|POST|PUT|DELETE|PATCH|OPTIONS|HEAD|Any)\(\s*["']([^"']+)["']\s*,\s*(?:[a-zA-Z0-9_\.]+\s*,\s*)*([a-zA-Z0-9_\.]+)`)
	goMuxRegex   = regexp.MustCompile(`(?m)\b(?:r|router|http)\.HandleFunc\(\s*["']([^"']+)["']\s*,\s*([a-zA-Z0-9_\.]+)`)

	// Python: @app.get("/path") or @router.post("/path") above def handler(
	pyRouteDecorator = regexp.MustCompile(`(?m)@(?:app|router|bp|api)\.(get|post|put|delete|patch|route)\(\s*["']([^"']+)["'](?:[^\)]*)\)`)
	pyFuncDef        = regexp.MustCompile(`(?m)^[ \t]*def\s+([a-zA-Z0-9_]+)\s*\(`)

	// TS/JS: app.get('/path', handler) or router.post('/path', handler)
	tsRouteRegex = regexp.MustCompile(`(?m)\b(?:app|router|api|v1|fastify|server)\.(get|post|put|delete|patch|all)\(\s*["']([^"']+)["']\s*,\s*(?:[a-zA-Z0-9_]+,\s*)*([a-zA-Z0-9_]+)`)
	// NestJS: @Controller('prefix') and @Get('path')
	nestControllerRegex = regexp.MustCompile(`(?m)@Controller\(\s*(?:["']([^"']*)["'])?\s*\)`)
	nestRouteRegex      = regexp.MustCompile(`(?m)@(Get|Post|Put|Delete|Patch|Options|Head|All)\(\s*(?:["']([^"']*)["'])?\s*\)`)
	tsMethodDef         = regexp.MustCompile(`(?m)^[ \t]*(?:public\s+|private\s+|protected\s+)?(?:async\s+)?([a-zA-Z0-9_]+)\s*\(`)
	// Next.js app router: export async function GET(req) in app/api/.../route.ts
	nextRouteRegex = regexp.MustCompile(`(?m)export\s+(?:async\s+)?function\s+(GET|POST|PUT|DELETE|PATCH|HEAD)\b`)

	// Java/Kotlin: @RequestMapping("/prefix") and @GetMapping("/path") above func
	springClassPrefixRegex = regexp.MustCompile(`(?m)@RequestMapping\(\s*(?:(?:value|path)\s*=\s*)?["']([^"']*)["']`)
	springRouteRegex       = regexp.MustCompile(`(?m)@(Get|Post|Put|Delete|Patch)Mapping(?:\(\s*(?:(?:value|path)\s*=\s*)?["']([^"']*)["']\s*\)|\(\s*\))?`)
	javaMethodDef          = regexp.MustCompile(`(?m)(?:public|private|protected)?\s+(?:[a-zA-Z0-9_<>[\]]+\s+)+([a-zA-Z0-9_]+)\s*\(`)
)

// stripCommentsKeepLines blanks out line and block comments with spaces while
// preserving newlines and character offsets so regex matches and line numbers stay exact.
func stripCommentsKeepLines(source string, isPython bool) string {
	b := []byte(source)
	n := len(b)
	i := 0
	for i < n {
		ch := b[i]
		// Handle string literals so comment markers inside strings are ignored
		if ch == '"' || ch == '\'' || (!isPython && ch == '`') {
			quote := ch
			i++
			for i < n {
				if b[i] == '\\' && quote != '`' {
					i += 2
					continue
				}
				if b[i] == quote {
					i++
					break
				}
				i++
			}
			continue
		}
		if isPython {
			if ch == '#' {
				for i < n && b[i] != '\n' {
					b[i] = ' '
					i++
				}
				continue
			}
		} else {
			if ch == '/' && i+1 < n && b[i+1] == '/' {
				for i < n && b[i] != '\n' {
					b[i] = ' '
					i++
				}
				continue
			}
			if ch == '/' && i+1 < n && b[i+1] == '*' {
				b[i] = ' '
				b[i+1] = ' '
				i += 2
				for i < n {
					if b[i] == '*' && i+1 < n && b[i+1] == '/' {
						b[i] = ' '
						b[i+1] = ' '
						i += 2
						break
					}
					if b[i] != '\n' {
						b[i] = ' '
					}
					i++
				}
				continue
			}
		}
		i++
	}
	return string(b)
}

func joinRoutePaths(prefix, sub string) string {
	p := strings.TrimSpace(prefix)
	s := strings.TrimSpace(sub)
	if p == "" && s == "" {
		return "/"
	}
	if p == "" {
		if !strings.HasPrefix(s, "/") {
			return "/" + s
		}
		return s
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	p = strings.TrimRight(p, "/")
	if s == "" || s == "/" {
		if p == "" {
			return "/"
		}
		return p
	}
	s = strings.TrimLeft(s, "/")
	return p + "/" + s
}

func makeRouteID(cleanPath, method, rPath string) string {
	return "route:" + cleanPath + ":" + method + ":" + rPath
}

// ExtractRoutesFromFile extracts HTTP route declarations and handler connections from source code.
func ExtractRoutesFromFile(filePath string, sourceCode string) []RouteEndpoint {
	var routes []RouteEndpoint
	cleanPath := strings.ReplaceAll(filePath, "\\", "/")
	ext := strings.ToLower(filepath.Ext(cleanPath))
	code := stripCommentsKeepLines(sourceCode, ext == ".py")

	switch ext {
	case ".go":
		// Gin / Echo / Fiber / Chi
		matches := goRouteRegex.FindAllStringSubmatchIndex(code, -1)
		for _, m := range matches {
			method := strings.ToUpper(code[m[2]:m[3]])
			rPath := code[m[4]:m[5]]
			handler := code[m[6]:m[7]]
			if idx := strings.LastIndex(handler, "."); idx >= 0 {
				handler = handler[idx+1:]
			}
			line := lineNumber(code, m[0])
			routes = append(routes, RouteEndpoint{
				ID:          makeRouteID(cleanPath, method, rPath),
				Method:      method,
				Path:        rPath,
				HandlerName: handler,
				FilePath:    cleanPath,
				Line:        line,
				Framework:   "go-web",
			})
		}
		// net/http & Mux HandleFunc
		muxMatches := goMuxRegex.FindAllStringSubmatchIndex(code, -1)
		for _, m := range muxMatches {
			rPath := code[m[2]:m[3]]
			handler := code[m[4]:m[5]]
			if idx := strings.LastIndex(handler, "."); idx >= 0 {
				handler = handler[idx+1:]
			}
			line := lineNumber(code, m[0])
			routes = append(routes, RouteEndpoint{
				ID:          makeRouteID(cleanPath, "ANY", rPath),
				Method:      "ANY",
				Path:        rPath,
				HandlerName: handler,
				FilePath:    cleanPath,
				Line:        line,
				Framework:   "net/http",
			})
		}

	case ".py":
		lines := strings.Split(code, "\n")
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if m := pyRouteDecorator.FindStringSubmatch(trimmed); len(m) >= 3 {
				method := strings.ToUpper(m[1])
				rPath := m[2]
				// Look ahead for def func_name
				handler := ""
				for j := i + 1; j < len(lines) && j <= i+5; j++ {
					if fnMatch := pyFuncDef.FindStringSubmatch(lines[j]); len(fnMatch) >= 2 {
						handler = fnMatch[1]
						break
					}
				}
				routes = append(routes, RouteEndpoint{
					ID:          makeRouteID(cleanPath, method, rPath),
					Method:      method,
					Path:        rPath,
					HandlerName: handler,
					FilePath:    cleanPath,
					Line:        i + 1,
					Framework:   "fastapi/flask",
				})
			}
		}

	case ".ts", ".js", ".tsx", ".jsx":
		// Express / Koa / Fastify
		matches := tsRouteRegex.FindAllStringSubmatchIndex(code, -1)
		for _, m := range matches {
			method := strings.ToUpper(code[m[2]:m[3]])
			rPath := code[m[4]:m[5]]
			handler := code[m[6]:m[7]]
			line := lineNumber(code, m[0])
			routes = append(routes, RouteEndpoint{
				ID:          makeRouteID(cleanPath, method, rPath),
				Method:      method,
				Path:        rPath,
				HandlerName: handler,
				FilePath:    cleanPath,
				Line:        line,
				Framework:   "express/fastify",
			})
		}
		// NestJS @Controller + @Get/@Post
		if ctrlMatch := nestControllerRegex.FindStringSubmatch(code); len(ctrlMatch) >= 1 {
			prefix := ""
			if len(ctrlMatch) >= 2 {
				prefix = ctrlMatch[1]
			}
			lines := strings.Split(code, "\n")
			for i, line := range lines {
				if m := nestRouteRegex.FindStringSubmatch(line); len(m) >= 2 {
					method := strings.ToUpper(m[1])
					subPath := ""
					if len(m) >= 3 {
						subPath = m[2]
					}
					fullPath := joinRoutePaths(prefix, subPath)
					handler := ""
					for j := i + 1; j < len(lines) && j <= i+5; j++ {
						if fnMatch := tsMethodDef.FindStringSubmatch(lines[j]); len(fnMatch) >= 2 {
							handler = fnMatch[1]
							break
						}
					}
					routes = append(routes, RouteEndpoint{
						ID:          makeRouteID(cleanPath, method, fullPath),
						Method:      method,
						Path:        fullPath,
						HandlerName: handler,
						FilePath:    cleanPath,
						Line:        i + 1,
						Framework:   "nestjs",
					})
				}
			}
		}
		// Next.js App Router API route
		if strings.Contains(cleanPath, "/api/") || strings.HasSuffix(cleanPath, "route.ts") || strings.HasSuffix(cleanPath, "route.js") {
			nextMatches := nextRouteRegex.FindAllStringSubmatchIndex(code, -1)
			apiPath := deriveNextApiPath(cleanPath)
			for _, m := range nextMatches {
				method := strings.ToUpper(code[m[2]:m[3]])
				line := lineNumber(code, m[0])
				routes = append(routes, RouteEndpoint{
					ID:          makeRouteID(cleanPath, method, apiPath),
					Method:      method,
					Path:        apiPath,
					HandlerName: method,
					FilePath:    cleanPath,
					Line:        line,
					Framework:   "nextjs",
				})
			}
		}

	case ".java", ".kt":
		lines := strings.Split(code, "\n")
		classPrefix := ""
		for _, line := range lines {
			if strings.Contains(line, "class ") {
				break
			}
			if cm := springClassPrefixRegex.FindStringSubmatch(line); len(cm) >= 2 {
				classPrefix = cm[1]
			}
		}
		for i, line := range lines {
			if m := springRouteRegex.FindStringSubmatch(line); len(m) >= 2 {
				method := strings.ToUpper(m[1])
				subPath := ""
				if len(m) >= 3 {
					subPath = m[2]
				}
				fullPath := joinRoutePaths(classPrefix, subPath)
				handler := ""
				for j := i + 1; j < len(lines) && j <= i+6; j++ {
					if fnMatch := javaMethodDef.FindStringSubmatch(lines[j]); len(fnMatch) >= 2 {
						handler = fnMatch[1]
						break
					}
				}
				routes = append(routes, RouteEndpoint{
					ID:          makeRouteID(cleanPath, method, fullPath),
					Method:      method,
					Path:        fullPath,
					HandlerName: handler,
					FilePath:    cleanPath,
					Line:        i + 1,
					Framework:   "spring",
				})
			}
		}
	}

	return routes
}

func deriveNextApiPath(filePath string) string {
	idx := strings.Index(filePath, "/api/")
	if idx >= 0 {
		sub := filePath[idx:]
		sub = strings.TrimSuffix(sub, "/route.ts")
		sub = strings.TrimSuffix(sub, "/route.js")
		return sub
	}
	return "/" + filepath.Base(filePath)
}

func lineNumber(source string, charIdx int) int {
	if charIdx <= 0 || charIdx > len(source) {
		return 1
	}
	return strings.Count(source[:charIdx], "\n") + 1
}

type RouteManager struct {
	db *storage.GraphStorage
}

func NewRouteManager(db *storage.GraphStorage) *RouteManager {
	return &RouteManager{db: db}
}

// IndexRoutes persists extracted routes as 1st-class graph nodes and links them to handler symbols.
// Returns the node IDs of the indexed route nodes.
func (rm *RouteManager) IndexRoutes(routes []RouteEndpoint) []string {
	var createdIDs []string
	for _, r := range routes {
		routeID := r.ID
		if routeID == "" {
			routeID = makeRouteID(r.FilePath, r.Method, r.Path)
		}
		handlerID := ""
		if r.HandlerName != "" {
			// Find symbol in the same file first
			candID := "symbol:" + r.FilePath + ":" + r.HandlerName
			if _, exists := rm.db.GetNode(candID); exists {
				handlerID = candID
			} else {
				// Find anywhere by name
				matches := rm.db.FindSymbolsByName(r.HandlerName, r.FilePath)
				if len(matches) > 0 {
					handlerID, _ = matches[0]["id"].(string)
				}
			}
		}

		routeProps := map[string]any{
			"id":             routeID,
			"level":          "Route",
			"name":           r.Method + " " + r.Path,
			"method":         r.Method,
			"path":           r.Path,
			"handler_name":   r.HandlerName,
			"handler_symbol": handlerID,
			"file_path":      r.FilePath,
			"line":           r.Line,
			"framework":      r.Framework,
			"search_text":    r.Method + " " + r.Path + " " + r.HandlerName,
		}

		rm.db.CreateNode(routeID, []string{"Route", "Endpoint"}, routeProps)
		createdIDs = append(createdIDs, routeID)

		// Link File -> Route
		fileID := "file:" + r.FilePath
		rm.db.CreateEdge(fileID, routeID, string(models.EdgeContains), nil)

		// Link Route <-> Handler Symbol
		if handlerID != "" {
			rm.db.CreateEdge(routeID, handlerID, "HANDLED_BY", nil)
			rm.db.CreateEdge(handlerID, routeID, "EXPOSES_ROUTE", map[string]any{
				"method": r.Method,
				"path":   r.Path,
			})

			// Also record route tag on symbol
			if symNode, ok := rm.db.GetNode(handlerID); ok {
				var routeList []string
				switch existing := symNode["routes"].(type) {
				case []string:
					routeList = existing
				case []any:
					for _, item := range existing {
						if s, ok := item.(string); ok {
							routeList = append(routeList, s)
						}
					}
				}
				routeTag := r.Method + " " + r.Path
				hasTag := false
				for _, t := range routeList {
					if t == routeTag {
						hasTag = true
						break
					}
				}
				if !hasTag {
					routeList = append(routeList, routeTag)
					symNode["routes"] = routeList
					rm.db.CreateNode(handlerID, []string{"L3Symbol", "Symbol", "Function"}, symNode)
				}
			}
		}
	}
	return createdIDs
}

// FindRoutes searches for indexed HTTP routes by path pattern or method.
func (rm *RouteManager) FindRoutes(pathPattern string, method string) []RouteEndpoint {
	defer rm.db.BeginReadSession()()

	var results []RouteEndpoint
	cleanPattern := strings.ToLower(strings.TrimSpace(pathPattern))
	targetMethod := strings.ToUpper(strings.TrimSpace(method))

	routeNodes := rm.db.QueryNodes("Route", "", 0)
	for _, props := range routeNodes {
		id, _ := props["id"].(string)
		m, _ := props["method"].(string)
		p, _ := props["path"].(string)
		if targetMethod != "" && targetMethod != "ALL" && m != targetMethod && m != "ANY" {
			continue
		}

		if cleanPattern != "" && !strings.Contains(strings.ToLower(p), cleanPattern) {
			continue
		}

		hName, _ := props["handler_name"].(string)
		hID, _ := props["handler_symbol"].(string)
		fPath, _ := props["file_path"].(string)
		line := toInt(props["line"])
		fw, _ := props["framework"].(string)

		results = append(results, RouteEndpoint{
			ID:          id,
			Method:      m,
			Path:        p,
			HandlerName: hName,
			HandlerID:   hID,
			FilePath:    fPath,
			Line:        line,
			Framework:   fw,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Path != results[j].Path {
			return results[i].Path < results[j].Path
		}
		if results[i].Method != results[j].Method {
			return results[i].Method < results[j].Method
		}
		return results[i].FilePath < results[j].FilePath
	})

	return results
}

func toInt(v any) int {
	switch val := v.(type) {
	case int:
		return val
	case int64:
		return int(val)
	case float64:
		return int(val)
	}
	return 0
}
