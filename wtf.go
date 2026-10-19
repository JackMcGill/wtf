package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

type BranchSelection struct {
	Name  string
	IsNew bool
}

// todo: more descriptive name
type RepoLayout struct {
	Name    string
	Path    string
	Session string
}

func main() {
	checkDeps()

	// check if we are in a git repo
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	_, err := cmd.Output()
	if err != nil {
		fmt.Println("Not in a git repo")
		return
	}

	branch, err := getBranch()
	if err != nil {
		fmt.Println(err)
	}

	// determine repo layout
	layout, err := getRepoLayout(branch.Name)
	if err != nil {
		fmt.Println(err)
		return
	}

	// ensure a worktree or session does not already exist
	if err := checkAvailable(branch.Name, layout.Session); err != nil {
		fmt.Println(err)
		return
	}
	// create worktree
	if err := createWorktree(layout.Path, branch); err != nil {
		fmt.Println(err)
		return
	}
	// create tmux session
	if err := createTmuxSession(layout.Session, layout.Path); err != nil {
		fmt.Println(err)
		return
	}
}

// check if fzf, git and tmux are installed.
func checkDeps() {
	commands := [][]string{
		{"git", "--version"},
		{"fzf", "--version"},
		{"tmux", "-V"},
	}
	for _, args := range commands {
		cmd := exec.Command(args[0], args[1:]...)

		_, err := cmd.Output()
		if err != nil {
			fmt.Println(args[0] + " not installed.")
		}
	}
}

// todo: need to not show the currently checked out branch if inside a worktree - i think this is done?
func getBranch() (BranchSelection, error) {
	currentBranchCmd := exec.Command("git", "branch", "--show-current")
	currentBranchOutput, err := currentBranchCmd.Output()
	if err != nil {
		return BranchSelection{}, fmt.Errorf("failed to get current branch: %w", err)
	}

	currentBranch := strings.TrimSpace(string(currentBranchOutput))

	cmd := exec.Command(
		"git",
		"for-each-ref",
		"--format=%(refname)",
		"refs/heads",
		"refs/remotes",
	)

	output, err := cmd.Output()
	if err != nil {
		return BranchSelection{}, fmt.Errorf("failed to list branches: %w", err)
	}

	branches := make([]string, 0)
	seen := make(map[string]struct{})

	for ref := range strings.SplitSeq(strings.TrimSpace(string(output)), "\n") {
		branch := normalizeBranch(ref)

		if branch == "" || branch == currentBranch {
			continue
		}

		if _, exists := seen[branch]; exists {
			continue
		}

		seen[branch] = struct{}{}
		branches = append(branches, branch)
	}

	const createNew = "Create New Branch..."

	branches = slices.Insert(branches, 0, createNew)

	cmd = exec.Command("fzf")
	cmd.Stdin = strings.NewReader(strings.Join(branches, "\n"))

	output, err = cmd.Output()
	if err != nil {
		return BranchSelection{}, err
	}

	selected := strings.TrimSpace(string(output))
	fmt.Println("Selected:", selected)

	if selected == createNew {
		reader := bufio.NewReader(os.Stdin)
		fmt.Print("Branch name: ")

		input, err := reader.ReadString('\n')
		if err != nil {
			return BranchSelection{}, err
		}

		return BranchSelection{
			Name:  sanitizeBranchName(input),
			IsNew: true,
		}, nil
	}

	return BranchSelection{
		Name:  selected,
		IsNew: false,
	}, nil
}

func normalizeBranch(ref string) string {
	ref = strings.TrimSpace(ref)

	switch {
	case strings.HasPrefix(ref, "refs/heads/"):
		return strings.TrimPrefix(ref, "refs/heads/")

	case strings.HasPrefix(ref, "refs/remotes/origin/"):
		branch := strings.TrimPrefix(ref, "refs/remotes/origin/")

		// Don't offer origin/HEAD as a branch.
		if branch == "HEAD" {
			return ""
		}

		return branch

	default:
		return ""
	}
}

func sanitizeBranchName(branch string) string {
	// Replace every character that is not a letter, number, `.` `_` or `-` with `-`.
	var invalidPathChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

	return strings.Trim(
		invalidPathChars.ReplaceAllString(strings.TrimSpace(branch), "-"),
		"-.",
	)
}

func getRepoLayout(branch string) (RepoLayout, error) {
	// Get the shared Git directory.
	cmd := exec.Command(
		"git",
		"rev-parse",
		"--path-format=absolute",
		"--git-common-dir",
	)

	output, err := cmd.Output()
	if err != nil {
		return RepoLayout{}, fmt.Errorf("failed to determine git directory: %w", err)
	}

	gitCommonDir := strings.TrimSpace(string(output))

	// Ask Git whether the common directory itself is a bare repository.
	cmd = exec.Command(
		"git",
		"--git-dir", gitCommonDir,
		"rev-parse",
		"--is-bare-repository",
	)

	output, err = cmd.Output()
	if err != nil {
		return RepoLayout{}, fmt.Errorf("failed to determine repository type: %w", err)
	}

	isBare := strings.TrimSpace(string(output)) == "true"

	var worktreeRoot string

	if isBare {
		// The bare repository itself is where its worktrees live.
		worktreeRoot = gitCommonDir
	} else {
		// For a normal repository, find its working tree root.
		cmd = exec.Command(
			"git",
			"rev-parse",
			"--path-format=absolute",
			"--show-toplevel",
		)

		output, err = cmd.Output()
		if err != nil {
			return RepoLayout{}, fmt.Errorf("failed to determine worktree root: %w", err)
		}

		worktreeRoot = strings.TrimSpace(string(output))
	}

	repoName := filepath.Base(worktreeRoot)

	var worktreeDir string

	if isBare {
		// Worktrees are children of the bare repository.
		worktreeDir = filepath.Join(worktreeRoot, sanitizeBranchName(branch))
	} else {
		// Worktrees are siblings of the normal repository.
		worktreeDir = filepath.Join(
			filepath.Dir(worktreeRoot),
			sanitizeBranchName(branch),
		)
	}

	//todo: put the branch in brackets
	sessionName := repoName + "-" + sanitizeBranchName(branch)

	return RepoLayout{
		Name:    repoName,
		Path:    worktreeDir,
		Session: sessionName,
	}, nil
}

func worktreeExists(branch string) (bool, error) {
	cmd := exec.Command("git", "worktree", "list", "--porcelain")

	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("failed to list worktrees: %w", err)
	}

	target := "branch refs/heads/" + branch

	if slices.Contains(strings.Split(string(output), "\n"), target) {
		return true, nil
	}

	return false, nil
}

func tmuxSessionExists(sessionName string) bool {
	cmd := exec.Command("tmux", "has-session", "-t", sessionName)
	return cmd.Run() == nil
}

func checkAvailable(branch string, sessionName string) error {
	exists, err := worktreeExists(branch)
	if err != nil {
		return err
	}

	if exists {
		return fmt.Errorf(
			"worktree already exists for branch %q",
			branch,
		)
	}

	if tmuxSessionExists(sessionName) {
		return fmt.Errorf(
			"tmux session %q already exists",
			sessionName,
		)
	}

	return nil
}

func createWorktree(path string, branch BranchSelection) error {
	var cmd *exec.Cmd

	if branch.IsNew {
		cmd = exec.Command(
			"git",
			"worktree",
			"add",
			"-b", branch.Name,
			path,
		)
	} else {
		cmd = exec.Command(
			"git",
			"worktree",
			"add",
			path,
			branch.Name,
		)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to create worktree: %w", err)
	}
	return nil
}

// todo: make it so that the 'code' window is the one we drop into when first connecting to the session
func createTmuxSession(sessionName string, path string) error {
	cmd := exec.Command(
		"tmux",
		"new-session",
		"-d",
		"-s", sessionName,
		"-c", path,
		"-n", "code", // this is the name of the first window in the session
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to create tmux session: %w", err)
	}

	// create the extra windows
	windows := []string{
		"server",
		"shell",
	}
	for _, window := range windows {
		cmd := exec.Command(
			"tmux",
			"new-window",
			"-t",
			sessionName,
			"-n",
			window,
			"-c",
			path,
		)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to create tmux window %q: %w", window, err)
		}
	}

	// Ensure the code window is active.
	cmd = exec.Command(
		"tmux",
		"select-window",
		"-t", sessionName+":code",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to select code window: %w", err)
	}

	// Switch to the new session if already inside tmux;
	// otherwise, attach the current terminal.
	var attachCmd *exec.Cmd

	if os.Getenv("TMUX") != "" {
		attachCmd = exec.Command(
			"tmux",
			"switch-client",
			"-t", sessionName,
		)
	} else {
		attachCmd = exec.Command(
			"tmux",
			"attach-session",
			"-t", sessionName,
		)
	}

	attachCmd.Stdin = os.Stdin
	attachCmd.Stdout = os.Stdout
	attachCmd.Stderr = os.Stderr

	if err := attachCmd.Run(); err != nil {
		return fmt.Errorf("failed to switch to tmux session %q: %w", sessionName, err)
	}

	return nil
}
