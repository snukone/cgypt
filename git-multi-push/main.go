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

func checkoutRepo(url, branch, targetDir string, dryRun bool, wg *sync.WaitGroup, logger *log.Logger) {
	defer wg.Done()

	if _, err := os.Stat(targetDir); err == nil {
		logger.Printf("[%s] Verzeichnis existiert, übersprungen\n", targetDir)
		return
	}

	args := []string{"clone"}
	if branch != "" {
		args = append(args, "-b", branch)
	}
	args = append(args, url, targetDir)

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
	dryRun := flag.Bool("dry", false, "Dry-Run (nur anzeigen, keine Änderungen)")
	branches := flag.String("branch", "", "Erlaubte Branches, Komma-getrennt")
	repoFile := flag.String("repo-file", "", "Textdatei mit Repo-Namen für commit/push oder URLs für checkout")
	parallel := flag.Int("parallel", 8, "Anzahl paralleler Jobs")
	checkout := flag.Bool("checkout", false, "Repos aus Liste auschecken")
	flag.Parse()

	if *repoFile == "" {
		fmt.Println("Bitte -repo-file angeben!")
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
		for _, url := range lines {
			targetDir := strings.TrimSuffix(filepath.Base(url), ".git")
			wg.Add(1)
			sem <- struct{}{}
			go func(url, dir string) {
				defer func() { <-sem }()
				checkoutRepo(url, "", dir, *dryRun, &wg, logger)
			}(url, targetDir)
		}
	} else {
		if flag.NArg() < 1 {
			fmt.Println("Bitte Commit Message angeben!")
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
