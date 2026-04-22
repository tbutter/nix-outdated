package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
)

type Package struct {
	Repo        string   `json:"repo"`
	SrcName     string   `json:"srcname"`
	Version     string   `json:"version"`
	Status      string   `json:"status"`
	VisibleName string   `json:"visiblename"`
	Maintainers []string `json:"maintainers"`
}

func main() {
	flag.Parse()

	resultFile, err := os.Create("result.txt")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating result.txt: %v\n", err)
		os.Exit(1)
	}
	defer resultFile.Close()
	resultNoPRFile, err := os.Create("result-nopr.txt")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating result-nopr.txt: %v\n", err)
		os.Exit(1)
	}
	defer resultNoPRFile.Close()
	prTitles, err := getNixpkgsPRTitles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching PR titles: %v\n", err)
		os.Exit(1)
	}
	prPackages := make(map[string]string)
	for _, title := range prTitles {
		if !strings.Contains(title, ":") {
			continue
		}
		parts := strings.Split(title, ":")
		prPackages[parts[0]] = title
	}
	fmt.Printf("Found %d PRs.\n\n", len(prPackages))
	for _, arg := range flag.Args() {
		runForMaintainer(arg, resultFile, resultNoPRFile, prPackages)
	}
}

func runForMaintainer(maintainer string, resultFile *os.File, resultNoPRFile *os.File, prPackages map[string]string) {
	fmt.Fprintf(resultFile, "\n\nOutdated packages in nix_unstable by %s:\n", maintainer)
	fmt.Fprintln(resultFile, "----------------------------------")
	fmt.Fprintf(resultNoPRFile, "\n\nOutdated packages in nix_unstable by %s without PR:\n", maintainer)
	fmt.Fprintln(resultNoPRFile, "----------------------------------")

	count := 0
	retry := 0

	rurl := "https://repology.org/api/v1/projects/?maintainer=" + url.QueryEscape(maintainer) + "&inrepo=nix_unstable&outdated=1"
	for rurl != "" {
		log.Printf("Fetching URL: %s\n", rurl)
		req, err := http.NewRequest("GET", rurl, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating request: %v\n", err)
			os.Exit(1)
		}

		// Repology requests a descriptive User-Agent
		req.Header.Set("User-Agent", "nix-outdated/1.0 (https://github.com/tbutter/nix-outdated)")

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching data: %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			fmt.Printf("Unexpected status code: %d for URL %s\n", resp.StatusCode, rurl)
			retry++
			if retry < 3 {
				fmt.Printf("Retrying...\n")
				time.Sleep(15 * time.Second)
				continue
			}
			os.Exit(1)
		}

		retry = 0

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading response body: %v\n", err)
			os.Exit(1)
		}

		var projects map[string][]Package
		if err := json.Unmarshal(body, &projects); err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing JSON: %v\n", err)
			os.Exit(1)
		}

		lastProject := ""
		lastLine := ""
		for _, projectName := range slices.Sorted(maps.Keys(projects)) {
			packages := projects[projectName]
			if strings.Compare(projectName, lastProject) > 0 {
				lastProject = projectName
			}
			for _, pkg := range packages {
				if pkg.Repo == "nix_unstable" {
					hasDifferentMaintainer := false
					for _, m := range pkg.Maintainers {
						if m != "fallback-mnt-nix@repology" {
							hasDifferentMaintainer = true
							break
						}
					}
					if !hasDifferentMaintainer {
						line := fmt.Sprintf("Project: %s | Name: %s | Version %s", projectName, pkg.SrcName, pkg.Version)
						if lastLine == line {
							continue
						}
						lastLine = line
						if prPackages[projectName] != "" {
							line = line + fmt.Sprintf(" | PR: %s", prPackages[projectName])
							fmt.Fprintln(resultFile, line)
						} else {
							fmt.Fprintln(resultNoPRFile, line)
							fmt.Fprintln(resultFile, line)
						}
						count++
					}
				}
			}
		}
		if len(projects) > 10 {
			rurl = "https://repology.org/api/v1/projects/" + url.QueryEscape(lastProject) + "/?maintainer=" + url.QueryEscape(maintainer) + "&inrepo=nix_unstable&outdated=1"
			time.Sleep(5 * time.Second)
		} else {
			rurl = ""
		}
	}
	fmt.Printf("\nTotal outdated packages found in nix_unstable: %d\n", count)

}

type PullRequest struct {
	Title string `json:"title"`
}

func getNixpkgsPRTitles() ([]string, error) {
	var titles []string
	url := "https://api.github.com/repos/NixOS/nixpkgs/pulls?state=open&per_page=100"
	client := &http.Client{}

	retry := 0
	for url != "" {
		log.Printf("Fetching URL: %s\n", url)
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github.v3+json")
		if token := os.Getenv("GITHUB_TOKEN"); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			if retry < 3 {
				fmt.Printf("GitHub API returned status %d for URL %s, retrying...\n", resp.StatusCode, url)
				retry++
				time.Sleep(5 * time.Second)
				continue
			}
			return nil, fmt.Errorf("GitHub API returned status %d for URL %s, had %d results", resp.StatusCode, url, len(titles))
		}

		retry = 0

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		var prs []PullRequest
		if err := json.Unmarshal(body, &prs); err != nil {
			return nil, err
		}

		for _, pr := range prs {
			titles = append(titles, pr.Title)
		}

		url = getNextPageURL(resp.Header.Get("Link"))
	}

	return titles, nil
}

func getNextPageURL(linkHeader string) string {
	for _, link := range strings.Split(linkHeader, ",") {
		parts := strings.Split(strings.TrimSpace(link), ";")
		if len(parts) >= 2 && strings.TrimSpace(parts[1]) == `rel="next"` {
			return strings.Trim(strings.TrimSpace(parts[0]), "<>")
		}
	}
	return ""
}
