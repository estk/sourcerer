package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/fatih/color"
	"gopkg.in/yaml.v2"
)

var (
	repoRE   = regexp.MustCompile("^github.com/([^/]*)/([^/]*)")
	semverRE = regexp.MustCompile(`^\D*(?P<first>(\d+))(\.(?P<second>\d+))?(\.(?P<third>\d+))?(\.(?P<fourth>\d+))?(\.(?P<fifth>\d+))?$`)
)

const (
	manifestName = "SOURCES"
	outFormat    = "{{.Name}}-{{.Version}}.{{.Ext}}"
)

type SourceEntry struct {
	Repo string
	Tag  string
	URL  string
}
type Config struct {
	Sources []SourceEntry
}

func main() {
	flag.Parse()
	root := flag.Arg(0)
	if root == "" {
		root = "."
	}

	manifests := searchForManifests(root)
	fmt.Println("Found manifests:")
	fmt.Println(strings.Join(manifests, "\n"), "\n")
	var wg sync.WaitGroup
	wg.Add(len(manifests))
	for _, m := range manifests {
		go func(m string) {
			handleManifest(m)
			wg.Done()
		}(m)
	}
	wg.Wait()
}

func handleManifest(filename string) {
	// find
	conf, err := parseConfig(filename)
	if err != nil {
		panic(err)
	}
	msgs, err := checkNewer(conf)
	if err != nil {
		panic(err)
	}

	fmt.Println(strings.Join(msgs, "\n"))
}

func searchForManifests(root string) []string {
	manifests := []string{}
	visit := func(path string, f os.FileInfo, err error) error {
		if strings.HasSuffix(path, manifestName) && !f.IsDir() {
			manifests = append(manifests, path)
		}
		return err
	}
	err := filepath.Walk(root, visit)
	if err != nil {
		panic(err)
	}
	return manifests
}

func checkEntry(e SourceEntry) (string, error) {
	if len(e.URL) != 0 {
		return fmt.Sprintf("Raw url specified, cannot check for currency: %s", e.URL), nil
	}
	owner, gitrepo, err := parseRepo(e.Repo)
	if err != nil {
		return "", err
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, gitrepo)
	res, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("There was an error retrieving the latest release for %s\n%v", e.Repo, err)
	}
	bodyBs, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return "", fmt.Errorf("unable to read body of url %s\n%v", e.URL, err)
	}
	var gitObj map[string]*json.RawMessage
	err = json.Unmarshal(bodyBs, &gitObj)
	if err != nil {
		return "", fmt.Errorf("unable to parse body of url %s\n%v\n body:\n%s", e.URL, err, string(bodyBs))
	}

	if gitObj["name"] == nil {
		m := color.YellowString("Unable to check currency, latest release undefined for %s", e.Repo)
		return m, nil
	}
	var tag string
	err = json.Unmarshal(*gitObj["name"], tag)
	rel, err := compareSemver(e.Tag, tag)
	if err != nil {
		return "", err
	}
	if rel < 0 {
		m := color.RedString(`There is a newer version of: %s
			have: %s
			latest: %s`, e.Repo, e.Tag, tag)
		return m, nil
	} else {
		m := color.GreenString("Up to date: %s", e.Repo)
		return m, nil
	}
}

func checkNewer(config Config) ([]string, error) {
	msgs := []string{}
	for _, e := range config.Sources {
		m, err := checkEntry(e)
		if err != nil {
			return msgs, err
		}
		msgs = append(msgs, m)
	}
	return msgs, nil
}

func mkSemver(s string) ([]int, error) {
	names := semverRE.SubexpNames()
	m := semverRE.FindStringSubmatch(s)
	out := []int{}
	for i, n := range names {
		if n != "" && len(m) > i && m[i] != "" {
			part, err := strconv.ParseInt(m[i], 10, 32)
			if err != nil {
				return out, fmt.Errorf("could not parse %s as semver\n%v", s, err)
			}
			if part < 0 {
				return out, fmt.Errorf("could not parse %s as semver, one part was < 0", s)
			}
			out = append(out, int(part))
		}
	}
	return out, nil
}

func compareSemver(x, y string) (int, error) {
	var as, bs []int
	xs, err1 := mkSemver(x)
	ys, err2 := mkSemver(y)
	if err1 != nil || err2 != nil {
		return 0, fmt.Errorf("Error comparing semver:\n%v\n%v", err1, err2)
	}

	// So we range over all parts
	if len(xs) >= len(ys) {
		as = xs
		bs = ys
	} else {
		as = ys
		bs = xs
	}
	for i, a := range as {
		var b int
		if i >= len(bs) {
			b = 0 // Nothing left to compare
		} else {
			b = bs[i]
		}

		if a > b {
			return 1, nil
		} else if a < b {
			return -1, nil
		}
	}
	return 0, nil
}

func parseRepo(repo string) (string, string, error) {
	match := repoRE.FindStringSubmatch(repo)
	if len(match) != 3 {
		return "", "", fmt.Errorf("Could not parse: %s, found: %v", repo, match)
	}
	return match[1], match[2], nil
}

func parseConfig(filename string) (Config, error) {
	var config Config

	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return config, err
	}

	err = yaml.Unmarshal(data, &config)
	if err != nil {
		return config, fmt.Errorf("Invalid yaml\n%v", err)
	}
	err = validateConfig(config)
	if err != nil {
		return config, fmt.Errorf("Invalid config\n%v", err)
	}

	return config, err
}

func validateConfig(config Config) error {
	for _, e := range config.Sources {
		if len(e.URL) != 0 && len(e.Repo) != 0 {
			return errors.New("cannot define a url and a repo; pick one")
		}
		if len(e.Repo) != 0 && len(e.Tag) == 0 {
			return errors.New("when defining a repo you must also define a tag to pull")
		}
	}
	return nil
}
