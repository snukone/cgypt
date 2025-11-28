package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

func run(cmd *exec.Cmd, dryRun bool) error {
	if dryRun {
		fmt.Printf("[Dry-Run] %s %s\n", cmd.Path, strings.Join(cmd.Args[1:], " "))
		return nil
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func isGitRepo(path string) bool {
	info, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil && info.IsDir()
}

func hasChanges(repo string) bool {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return len(out) > 0
}

func getCurrentBranch(repo string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func processRepo(repo, commitMessage string, allowedBranches []string, dryRun bool, wg *sync.WaitGroup, logger *log.Logger) {
	defer wg.Done()

	if !isGitRepo(repo) {
		logger.Printf("[%s] Kein Git-Repo, übersprungen\n", repo)
		return
	}

	branch, err := getCurrentBranch(repo)
	if err != nil {
		logger.Printf("[%s] Fehler beim Branch-Check: %v\n", repo, err)
		return
	}

	if len(allowedBranches) > 0 {
		match := false
		for _, b := range allowedBranches {
			if b == branch {
				match = true
				break
			}
		}
		if !match {
			logger.Printf("[%s] Übersprungen: Branch '%s' nicht erlaubt\n", repo, branch)
			return
		}
	}

	if !hasChanges(repo) {
		logger.Printf("[%s] Keine Änderungen, übersprungen\n", repo)
		return
	}

	logger.Printf("[%s] Bearbeite Repo auf Branch '%s'\n", repo, branch)

	cmds := [][]string{
		{"git", "add", "-A"},
		{"git", "commit", "-m", commitMessage},
		{"git", "push"},
	}

	for _, c := range cmds {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Dir = repo
		if err := run(cmd, dryRun); err != nil {
			logger.Printf("[%s] Fehler: %v\n", repo, err)
			break
		}
	}
}

func checkoutRepo(providerURL, repoName, branch, targetDir string, dryRun bool, wg *sync.WaitGroup, logger *log.Logger) {
	defer wg.Done()

	if _, err := os.Stat(targetDir); err == nil {
		logger.Printf("[%s] Verzeichnis existiert, übersprungen\n", targetDir)
		return
	}

	fullURL := strings.TrimRight(providerURL, "/") + "/" + repoName + ".git"
	args := []string{"clone"}
	if branch != "" {
		args = append(args, "-b", branch)
	}
	args = append(args, fullURL, targetDir)

	cmd := exec.Command("git", args...)
	if err := run(cmd, dryRun); err != nil {
		logger.Printf("[%s] Checkout Fehler: %v\n", targetDir, err)
		return
	}
	logger.Printf("[%s] Erfolgreich ausgecheckt\n", targetDir)
}

func readLines(filePath string) ([]string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines, scanner.Err()
}

func main() {
	// Flags
	dryRun := flag.Bool("dry", false, "Dry-Run: zeigt nur Befehle, führt sie nicht aus")
	branches := flag.String("branch", "", "Erlaubte Branches, Komma-getrennt")
	repoFile := flag.String("repo-file", "", "Textdatei mit Repo-Namen (eine Zeile = ein Repo)")
	parallel := flag.Int("parallel", 8, "Anzahl paralleler Jobs")
	checkout := flag.Bool("checkout", false, "Repos aus Liste auschecken")
	providerURL := flag.String("provider-url", "", "Git Provider Basis-URL (z.B. https://bitbucket.org/meinteam)")

	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Git-Multi-Tool\n\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [flags] \"Commit Message\"\n\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "Flags:\n")
		flag.PrintDefaults()
		fmt.Fprintf(flag.CommandLine.Output(), "\nExamples:\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  # Commit & Push auf alle Repos im repo-file, nur Branch main oder develop, Dry-Run\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  %s -repo-file repos.txt -branch main,develop -dry \"Update all repos\"\n\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "  # Checkout aller Repos aus Liste, mit Bitbucket URL, max 8 parallel\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  %s -repo-file repos.txt -checkout -provider-url https://bitbucket.org/meinteam -parallel 8\n\n", os.Args[0])
	}

	flag.Parse()

	if *repoFile == "" {
		fmt.Println("Fehler: Bitte -repo-file angeben!")
		flag.Usage()
		return
	}

	if *checkout && *providerURL == "" {
		fmt.Println("Fehler: Bitte -provider-url für Checkout angeben!")
		flag.Usage()
		return
	}

	allowedBranches := []string{}
	if *branches != "" {
		allowedBranches = strings.Split(*branches, ",")
	}

	logFile, err := os.OpenFile("git-multi-push.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Println("Konnte Log-Datei nicht öffnen:", err)
		return
	}
	defer logFile.Close()
	logger := log.New(logFile, "", log.LstdFlags)
	logger.Printf("=== Starte Operation: %s ===\n", time.Now().Format(time.RFC3339))

	lines, err := readLines(*repoFile)
	if err != nil {
		fmt.Println("Fehler beim Lesen der Datei:", err)
		return
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, *parallel)

	if *checkout {
		for _, repoName := range lines {
			targetDir := repoName
			wg.Add(1)
			sem <- struct{}{}
			go func(repoName, dir string) {
				defer func() { <-sem }()
				checkoutRepo(*providerURL, repoName, "", dir, *dryRun, &wg, logger)
			}(repoName, targetDir)
		}
	} else {
		if flag.NArg() < 1 {
			fmt.Println("Bitte Commit Message angeben!")
			flag.Usage()
			return
		}
		commitMessage := flag.Arg(0)
		for _, repo := range lines {
			wg.Add(1)
			sem <- struct{}{}
			go func(r string) {
				defer func() { <-sem }()
				processRepo(r, commitMessage, allowedBranches, *dryRun, &wg, logger)
			}(repo)
		}
	}

	wg.Wait()
	logger.Println("=== Alle Jobs fertig ===")
	fmt.Println("Alle Jobs fertig!")
}
