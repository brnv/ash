package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/bndr/gopencils"
	"github.com/docopt/docopt-go"
	"github.com/op/go-logging"
)

var (
	reStashURL = regexp.MustCompile(
		`(https?://.*/)` +
			`((users|projects)/([^/]+))` +
			`/repos/([^/]+)` +
			`/pull-requests/(\d+)`)
)

var configPath = os.Getenv("HOME") + "/.config/ash/ashrc"

var logger = logging.MustGetLogger("main")

const logFormat = "%{color}%{time:15:04:05.00} [%{level:.4s}] %{message}%{color:reset}"
const startUrlExample = "http[s]://<host>/(users|projects)/<project>/repos/<repo>/pull-requests/<id>"

type CmdLineArgs string

func parseCmdLine(cmd []string) (map[string]interface{}, error) {
	help := `Atlassian Stash Reviewer.

Most convient usage is specify pull request url and file you want to review:
  ash review ` + startUrlExample + ` file

However, you can set up --host and --project flags in ~/.config/ash/ashrc file
and access pull requests by shorthand commands:
  ash proj/mycoolrepo/1 review  # if --host is given
  ash mycoolrepo/1 review       # if --host and --project is given
  ash mycoolrepo ls             # --//--

Ash then open $EDITOR for commenting on pull request.

You can add comments by just specifying them after line you want to comment,
beginning with '# '.

You can delete comment by deleting it from file, and, of course, modify comment
you own by modifying it in the file.

After you finish your edits, just save file and exit from editor. Ash will
apply all changes made to the review.

If <file-name> is omitted, ash welcomes you to review the overview.

'ls' command can be used to list various things, including:
* files in pull request;
* opened/merged/declined pull requests for repo;
* repositories in specified project [NOT IMPLEMENTED];
* projects [NOT IMPLEMENTED];

Usage:
  ash [options] <project>/<repo>/<pr> review [<file-name>]
  ash [options] <project>/<repo>/<pr> ls
  ash [options] <project>/<repo> ls-reviews [-d] [(open|merged|declined)]
  ash -h | --help

Options:
  -h --help         Show this help.
  -u --user=<user>  Stash username.
  -p --pass=<pass>  Stash password. You want to set this flag in .ashrc file.
  -e=<editor>       Editor to use. This has priority over $EDITOR env var.
  --debug=<level>   Verbosity [default: 0].
  --url=<url>       Template URL where pull requests are available.
                    Usually you do not need to change that value.
                    [default: /{{.Ns}}/{{.Proj}}/repos/{{.Repo}}/pull-requests/{{.Pr}}]
  --host=<host>     Stash host name. Change to hostname your stash is located.
  --project=<proj>  Use to specify default project that can be used when serching
                    pull requests. Can be set in either <project> or
                    <project>/<repo> format.
`

	args, err := docopt.Parse(help, cmd, true, "0.1 beta", false)

	return args, err
}

func main() {
	rawArgs := mergeArgsWithConfig(configPath)
	args, _ := parseCmdLine(rawArgs)

	setupLogger(args)

	logger.Info("cmd line args are read from %s\n", configPath)
	logger.Debug("cmd line args: %s", CmdLineArgs(fmt.Sprintf("%s", rawArgs)))

	if args["--user"] == nil || args["--pass"] == nil {
		fmt.Println("--user and --pass should be specified.")
		os.Exit(1)
	}

	uri := parseUri(args)

	user := args["--user"].(string)
	pass := args["--pass"].(string)

	auth := gopencils.BasicAuth{user, pass}
	api := Api{uri.host, auth}
	project := Project{&api, uri.project}
	repo := project.GetRepo(uri.repo)

	switch {
	case args["<project>/<repo>/<pr>"] != nil:
		reviewMode(args, repo, uri.pr)
	case args["<project>/<repo>"] != nil:
		repoMode(args, repo)
	}
}

func setupLogger(args map[string]interface{}) {
	logging.SetBackend(logging.NewLogBackend(os.Stderr, "", 0))
	logging.SetFormatter(logging.MustStringFormatter(logFormat))

	logLevels := []logging.Level{
		logging.WARNING,
		logging.INFO,
		logging.DEBUG,
	}

	requestedLogLevel := int64(0)
	if args["--debug"] != nil {
		requestedLogLevel, _ = strconv.ParseInt(args["--debug"].(string), 10, 16)
	}

	for _, lvl := range logLevels[:requestedLogLevel+1] {
		logging.SetLevel(lvl, "main")
	}
}

func reviewMode(args map[string]interface{}, repo Repo, pr int64) {
	editor := os.Getenv("EDITOR")
	if args["-e"] != nil {
		editor = args["-e"].(string)
	}

	if editor == "" {
		fmt.Println(
			"Either -e or env var $EDITOR should specify edtitor to use.")
		os.Exit(1)
	}
	path := ""
	if args["<file-name>"] != nil {
		path = args["<file-name>"].(string)
	}

	pullRequest := repo.GetPullRequest(pr)

	switch {
	case args["ls"]:
		showFilesList(pullRequest)
	case args["review"]:
		review(pullRequest, editor, path)
	}
}

func repoMode(args map[string]interface{}, repo Repo) {
	switch {
	case args["ls-reviews"]:
		state := "open"
		switch {
		case args["declined"]:
			state = "declined"
		case args["merged"]:
			state = "merged"
		}
		showReviewsInRepo(repo, state, args["-d"].(bool))
	}
}

func showReviewsInRepo(repo Repo, state string, showDesc bool) {
	reviews, err := repo.ListPullRequest(state)

	if err != nil {
		logger.Critical("can not list reviews: %s", err.Error())
	}

	reBeginningOfLine := regexp.MustCompile("(?m)^")
	reBranchName := regexp.MustCompile("([^/]+)$")
	for _, r := range reviews {
		branchName := reBranchName.FindStringSubmatch(r.FromRef.Id)[1]
		pretext := fmt.Sprintf("%3d", r.Id)
		fmt.Printf("%s %s [%6s] %25s %-20s", pretext,
			r.State, r.UpdatedDate,
			r.Author.User.DisplayName,
			branchName)

		if showDesc && r.Description != "" {
			desc := fmt.Sprintf("\n---\n%s\n---\n", r.Description)
			fmt.Println(reBeginningOfLine.ReplaceAllString(
				desc,
				strings.Repeat(" ", len([]rune(pretext))+1)))
		}

		fmt.Println()
	}

	//log.Printf("%#v", reviews, err)
}

func parseUri(args map[string]interface{}) (
	result struct {
		host    string
		project string
		repo    string
		pr      int64
	},
) {
	uri := ""
	keyName := ""
	should := 0

	if args["<project>/<repo>/<pr>"] != nil {
		keyName = "<project>/<repo>/<pr>"
		uri = args[keyName].(string)
		should = 3
	}

	if args["<project>/<repo>"] != nil {
		keyName = "<project>/<repo>"
		uri = args[keyName].(string)
		should = 2
	}

	matches := reStashURL.FindStringSubmatch(uri)
	if len(matches) != 0 {
		result.host = matches[1]
		result.project = matches[2]
		result.repo = matches[5]
		result.pr, _ = strconv.ParseInt(matches[6], 10, 16)

		return result
	}

	if args["--host"] == nil {
		fmt.Println(
			"In case of shorthand syntax --host should be specified")
		os.Exit(1)
	}

	matches = strings.Split(uri, "/")

	result.host = args["--host"].(string)

	if len(matches) == 2 && should == 3 && args["--project"] != nil {
		result.repo = matches[0]
		result.pr, _ = strconv.ParseInt(matches[1], 10, 16)
	}

	if args["--project"] != nil {
		result.project = args["--project"].(string)
	}

	if len(matches) == 2 && should == 2 {
		result.project = matches[0]
		result.repo = matches[1]
	}

	if len(matches) >= 3 && should == 3 {
		result.project = matches[0]
		result.repo = matches[1]
		result.pr, _ = strconv.ParseInt(matches[2], 10, 16)
	}

	enough := result.project != "" && result.repo != "" &&
		(result.pr != 0 || should == 2)

	if !enough {
		fmt.Println(
			"<pull-request> should be in either:\n" +
				" - URL Format: " + startUrlExample + "\n" +
				" - Shorthand format: " + keyName,
		)
		os.Exit(1)
	}

	if result.project[0] == '~' || result.project[0] == '%' {
		result.project = "users/" + result.project[1:]
	} else {
		result.project = "projects/" + result.project
	}

	return result
}

func editReviewInEditor(
	editor string, reviewToEdit *Review, fileToUse *os.File,
) ([]ReviewChange, error) {
	logger.Info("writing review to file: %s", fileToUse.Name())

	AddUsageComment(reviewToEdit)
	AddVimModeline(reviewToEdit)

	WriteReview(reviewToEdit, fileToUse)

	fileToUse.Sync()

	logger.Debug("opening editor: %s %s", editor, fileToUse.Name())
	editorCmd := exec.Command(editor, fileToUse.Name())
	editorCmd.Stdin = os.Stdin
	editorCmd.Stdout = os.Stdout
	editorCmd.Stderr = os.Stderr

	err := editorCmd.Run()
	if err != nil {
		logger.Fatal(err)
	}

	fileToUse.Sync()
	fileToUse.Seek(0, os.SEEK_SET)

	logger.Debug("reading modified review back")
	editedReview, err := ReadReview(fileToUse)
	if err != nil {
		return nil, err
	}

	logger.Debug("comparing old and new reviews")
	return reviewToEdit.Compare(editedReview), nil
}

func mergeArgsWithConfig(path string) []string {
	args := make([]string, 0)

	conf, err := ioutil.ReadFile(path)

	if err != nil {
		logger.Warning("can not access config: %s", err.Error())
		return args
	}

	confLines := strings.Split(string(conf), "\n")
	for _, line := range confLines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		args = append(args, line)
	}

	args = append(args, os.Args[1:]...)

	return args
}

func showFilesList(pr PullRequest) {
	logger.Debug("showing list of files in PR")
	files, err := pr.GetFiles()
	if err != nil {
		logger.Error("error accessing Stash: %s", err.Error())
	}

	for _, file := range files {
		execFlag := ""
		if file.DstExec != file.SrcExec {
			if file.DstExec {
				execFlag = "-x"
			} else {
				execFlag = "+x"
			}
		}

		fmt.Printf("%2s %7s %s\n", execFlag, file.ChangeType, file.DstPath)
	}
}

func review(pr PullRequest, editor string, path string) {
	var review *Review
	var err error

	if path == "" {
		logger.Debug("downloading overview from Stash")
		review, err = pr.GetActivities()
	} else {
		logger.Debug("downloading review from Stash")
		review, err = pr.GetReview(path)
	}

	if err != nil {
		logger.Fatal(err)
	}

	if len(review.changeset.Diffs) == 0 {
		fmt.Println("Specified file is not found in pull request.")
		os.Exit(1)
	}

	tmpFile, err := ioutil.TempFile(os.TempDir(), "review.diff.")
	defer func() {
		if r := recover(); r != nil {
			printPanicMsg(r, tmpFile.Name())
		}
	}()

	changes, err := editReviewInEditor(editor, review, tmpFile)
	if err != nil {
		logger.Fatal(err)
	}

	if len(changes) == 0 {
		logger.Warning("no changes detected in review file (maybe a bug)")
		os.Exit(2)
	}

	logger.Debug("applying changes (%d)", len(changes))

	for i, change := range changes {
		fmt.Printf("(%d/%d) applying changes\n", i+1, len(changes))
		logger.Debug("change payload: %#v", change.GetPayload())
		err := pr.ApplyChange(change)
		if err != nil {
			logger.Critical("can not apply change: %s", err.Error())
		}
	}

	tmpFile.Close()
	os.Remove(tmpFile.Name())
	logger.Debug("removed tmp file: %s", tmpFile.Name())
}

func (p CmdLineArgs) Redacted() interface{} {
	rePassFlag := regexp.MustCompile(`(\s(-p|--pass)[\s=])([^ ]+)`)
	matches := rePassFlag.FindStringSubmatch(string(p))
	if len(matches) == 0 {
		return string(p)
	} else {
		return rePassFlag.ReplaceAllString(
			string(p),
			`$1`+logging.Redact(string(matches[3])))
	}
}

func printPanicMsg(r interface{}, reviewFileName string) {
	fmt.Println()
	fmt.Println("PANIC:", r)
	fmt.Println()
	fmt.Println(string(debug.Stack()))
	fmt.Println("Well, program is crashed. This is a bug.")
	fmt.Println()
	fmt.Printf("All data you've entered are kept in the file:\n\t%s",
		reviewFileName)
	fmt.Println()
	fmt.Println()
	fmt.Printf("Feel free to open issue or PR on the:\n\t%s",
		"https://github.com/seletskiy/ash")
	fmt.Println()
}
