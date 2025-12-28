package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/fsnotify/fsnotify"
)

// The Godbolt API accepts: "files": [{"filename": "helper.zig", "contents": "..."}]

type FileEntry struct {
	Filename string `json:"filename"`
	Contents string `json:"contents"`
}

type CompileRequest struct {
	Source  string         `json:"source"`
	Options CompileOptions `json:"options"`
	Files   []FileEntry    `json:"files,omitempty"`
}

type CompileOptions struct {
	UserArguments string  `json:"userArguments"`
	Filters       Filters `json:"filters"`
}

type Filters struct {
	Binary      bool `json:"binary"`
	CommentOnly bool `json:"commentOnly"`
	Demangle    bool `json:"demangle"`
	Directives  bool `json:"directives"`
	Intel       bool `json:"intel"`
	Labels      bool `json:"labels"`
	Trim        bool `json:"trim"`
}

type CompileResponse struct {
	Code   int          `json:"code"`
	Stdout []OutputLine `json:"stdout"`
	Stderr []OutputLine `json:"stderr"`
	Asm    []AsmLine    `json:"asm"`
}

type OutputLine struct {
	Text string `json:"text"`
}

type AsmLine struct {
	Text   string     `json:"text"`
	Source *AsmSource `json:"source,omitempty"`
}

type AsmSource struct {
	File *string `json:"file"`
	Line int     `json:"line"`
}

func clearScreen() {
	cmd := exec.Command("clear")
	cmd.Stdout = os.Stdout
	cmd.Run()
}

// collectProjectFiles gathers all source files from a directory for multi-file compilation
// searchDir: where to search for files (the -root flag or main file's directory)
// mainFile: the main source file (absolute path)
// relativeToDir: paths in output will be relative to this directory (usually main file's directory)
func collectProjectFiles(searchDir string, mainFile string, relativeToDir string) ([]FileEntry, error) {
	ext := filepath.Ext(mainFile)
	var files []FileEntry

	skipDirs := map[string]bool{
		".zig-cache": true, ".git": true, ".idea": true,
		"node_modules": true, "target": true, "zig-out": true,
	}

	err := filepath.WalkDir(searchDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		if filepath.Ext(path) != ext || path == mainFile || d.Name() == "build.zig" {
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		// Make path relative to the main file's directory (how Zig resolves imports)
		relPath, err := filepath.Rel(relativeToDir, path)
		if err != nil {
			return err
		}

		files = append(files, FileEntry{
			Filename: relPath,
			Contents: string(content),
		})
		return nil
	})

	return files, err
}

func highlight(code, language string) string {
	lexer := lexers.Get(language)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)

	style := styles.Get("gruvbox")
	if style == nil {
		style = styles.Fallback
	}

	formatter := formatters.Get("terminal256")
	if formatter == nil {
		formatter = formatters.Fallback
	}

	iterator, err := lexer.Tokenise(nil, code)
	if err != nil {
		return code
	}

	var buf bytes.Buffer
	if err := formatter.Format(&buf, style, iterator); err != nil {
		return code
	}

	return buf.String()
}

func getLangFromFile(filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".zig":
		return "zig"
	case ".c":
		return "c"
	case ".cpp", ".cc", ".cxx":
		return "cpp"
	case ".rs":
		return "rust"
	case ".go":
		return "go"
	case ".py":
		return "python"
	default:
		return ""
	}
}

func compile(baseURL, compiler, filePath, args string, showSource bool, projectRoot string) error {
	source, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	// Show highlighted source if requested
	if showSource {
		lang := getLangFromFile(filePath)
		fmt.Println("\033[36m━━━ Source ━━━\033[0m")
		fmt.Println(highlight(string(source), lang))
	}

	// Collect additional project files for multi-file compilation
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path: %w", err)
	}
	mainDir := filepath.Dir(absPath)

	// Determine search directory: use -root flag if provided, otherwise use main file's directory
	var searchDir string
	if projectRoot != "" {
		searchDir, err = filepath.Abs(projectRoot)
		if err != nil {
			return fmt.Errorf("failed to get absolute project root: %w", err)
		}
	} else {
		searchDir = mainDir
	}

	// Search from searchDir, but paths are relative to mainDir (how Zig resolves @import)
	projectFiles, err := collectProjectFiles(searchDir, absPath, mainDir)
	if err != nil {
		fmt.Printf("\033[33mWarning: could not collect project files: %v\033[0m\n", err)
		projectFiles = nil // Continue with just the main file
	}

	req := CompileRequest{
		Source: string(source),
		Files:  projectFiles,
		Options: CompileOptions{
			UserArguments: args,
			Filters: Filters{
				Binary:      false,
				CommentOnly: true,
				Demangle:    true,
				Directives:  true,
				Intel:       true,
				Labels:      true,
				Trim:        false,
			},
		},
	}

	jsonData, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/api/compiler/%s/compile", baseURL, compiler)

	httpReq, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	client := &http.Client{}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	var result CompileResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("failed to parse response: %w\nBody: %s", err, string(body[:min(500, len(body))]))
	}

	// Print stderr if any
	for _, line := range result.Stderr {
		fmt.Printf("\033[31m%s\033[0m\n", line.Text)
	}

	// Print stdout if any
	for _, line := range result.Stdout {
		fmt.Println(line.Text)
	}

	// Print assembly with syntax highlighting
	if len(result.Asm) > 0 {
		fmt.Println("\n\033[36m━━━ Assembly ━━━\033[0m")
		var asmBuilder strings.Builder
		for _, line := range result.Asm {
			asmBuilder.WriteString(line.Text)
			asmBuilder.WriteString("\n")
		}
		fmt.Print(highlight(asmBuilder.String(), "gas"))
	}

	return nil
}

func watch(baseURL, compiler, filePath, args string, showSource bool, projectRoot string) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create watcher: %w", err)
	}
	defer watcher.Close()

	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path: %w", err)
	}

	dir := filepath.Dir(absPath)
	if err := watcher.Add(dir); err != nil {
		return fmt.Errorf("failed to watch directory: %w", err)
	}

	fmt.Printf("\033[34m⚡ Watching %s\033[0m\n", filePath)
	fmt.Printf("\033[34m   Compiler: %s\033[0m\n", compiler)
	fmt.Printf("\033[34m   Args: %s\033[0m\n", args)
	fmt.Printf("\033[34m   Server: %s\033[0m\n\n", baseURL)

	// Initial compile
	if err := compile(baseURL, compiler, filePath, args, showSource, projectRoot); err != nil {
		fmt.Printf("\033[31mError: %v\033[0m\n", err)
	}

	// Debounce timer
	var debounce *time.Timer

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if event.Name == absPath && (event.Op&fsnotify.Write == fsnotify.Write || event.Op&fsnotify.Create == fsnotify.Create) {
				if debounce != nil {
					debounce.Stop()
				}
				debounce = time.AfterFunc(100*time.Millisecond, func() {
					clearScreen()
					fmt.Printf("\033[34m⚡ %s — %s\033[0m\n\n", filePath, time.Now().Format("15:04:05"))
					if err := compile(baseURL, compiler, filePath, args, showSource, projectRoot); err != nil {
						fmt.Printf("\033[31mError: %v\033[0m\n", err)
					}
				})
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			fmt.Printf("\033[31mWatcher error: %v\033[0m\n", err)
		}
	}
}

func main() {
	var (
		server      = flag.String("server", "https://godbolt.org", "Compiler Explorer server URL")
		compiler    = flag.String("compiler", "ztrunk", "Compiler ID (e.g., ztrunk, z0140, g141, clang1910)")
		args        = flag.String("args", "", "Compiler arguments (e.g., '-O ReleaseFast -target aarch64-macos')")
		once        = flag.Bool("once", false, "Compile once and exit (don't watch)")
		showSource  = flag.Bool("source", false, "Show highlighted source code")
		projectRoot = flag.String("root", "", "Project root for multi-file imports (default: file's directory)")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "cet - Compiler Explorer Terminal\n\n")
		fmt.Fprintf(os.Stderr, "Usage: cet [options] <file>\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  cet -args='-O ReleaseFast -target aarch64-macos -mcpu=apple_m4' main.zig\n")
		fmt.Fprintf(os.Stderr, "  cet -compiler=g132 -args='-O3' main.c\n")
		fmt.Fprintf(os.Stderr, "  cet -once -source main.zig\n")
		fmt.Fprintf(os.Stderr, "  cet -root=. src/main.zig   # Multi-file project with imports from repo root\n")
	}
	flag.Parse()

	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(1)
	}

	filePath := flag.Arg(0)

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: file %s does not exist\n", filePath)
		os.Exit(1)
	}

	if *once {
		if err := compile(*server, *compiler, filePath, *args, *showSource, *projectRoot); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := watch(*server, *compiler, filePath, *args, *showSource, *projectRoot); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
