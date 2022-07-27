// Copyright 2021 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package repo

import (
	"bufio"
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"reflect"

	"github.com/blevesearch/segment"
	"github.com/google/go-github/v33/github"
	"github.com/spf13/viper"
	"k8s.io/klog/v2"

	"github.com/google/pullsheet/pkg/client"
	"github.com/google/pullsheet/pkg/ghcache"
)

var notSegmentRe = regexp.MustCompile(`[/-_]+`)

// ReviewCommentSummary a summary of a users reviews on a PR
type ReviewSummary struct {
	URL            string
	Date           string
	Project        string
	Reviewers      []string
	PRAuthor       string
	Title          string
	Reviewer       string
	ReviewComments int
	Words          int
}

type ReviewCommentSummary struct {
	URL            string
	Date           string
	Project        string
	Reviewer       string
	PRAuthor       string
	PRComments     int
	ReviewComments int
	Words          int
	Title          string
}

type comment struct {
	Author    string
	Body      string
	Review    bool
	CreatedAt time.Time
}

func stringInSlice(a string, list []string) bool {
    for _, b := range list {
        if b == a {
            return true
        }
    }
    return false
}

// MergedReviews returns a list of pull requests in a project (merged only)
func MergedReviews(ctx context.Context, c *client.Client, org string, project string, since time.Time, until time.Time, users []string) ([]*ReviewSummary, error) {
	prs, err := MergedPulls(ctx, c, org, project, since, until, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("pulls: %v", err)
	}

	klog.Infof("found %d PR's in %s/%s to find reviews for", len(prs), org, project)
	reviews := []*ReviewSummary{}

	matchUser := map[string]bool{}
	for _, u := range users {
		matchUser[strings.ToLower(u)] = true
	}

	for _, pr := range prs {
		// username -> summary
		prMap := map[string]*ReviewCommentSummary{}
		prReviewsMap := map[int]*ReviewSummary{}
		comments := []comment{}

		klog.Infof("HELLO %d", pr.GetNumber())

		// There is wickedness in the GitHub API: PR comments are available via the Issues API, and PR *review* comments are available via the PullRequests API
		cs, err := ghcache.PullRequestsListComments(ctx, c.Cache, c.GitHubClient, pr.GetMergedAt(), org, project, pr.GetNumber())
		if err != nil {
			return nil, err
		}

		for idx := range cs {
			if isBot(cs[idx].GetUser()) {
				continue
			}

			body := strings.TrimSpace(cs[idx].GetBody())
			comments = append(comments, comment{Author: cs[idx].GetUser().GetLogin(), Body: body, CreatedAt: cs[idx].GetCreatedAt(), Review: false})
		}

		is, err := ghcache.PullRequestsListReviews(ctx, c.Cache, c.GitHubClient, pr.GetMergedAt(), org, project, pr.GetNumber())
		if err != nil {
			return nil, err
		}

		for count, i := range is {
			klog.Infof("LOLOL %d %d %s", pr.GetNumber(), count, i.GetUser().GetLogin())
			if isBot(i.GetUser()) {
				continue
			}

			body := strings.TrimSpace(i.GetBody())
			if (strings.HasPrefix(body, "/") || strings.HasPrefix(body, "cc")) && len(body) < 64 {
				klog.Infof("ignoring tag comment in %s: %q", i.GetHTMLURL(), body)
				continue
			}

			comments = append(comments, comment{Author: i.GetUser().GetLogin(), Body: body, CreatedAt: i.GetSubmittedAt(), Review: true})
		}

		for _, c := range comments {
			// klog.Infof("HELLO %d %d %s %s", pr.GetNumber(), i, c.Author, c)

			if c.CreatedAt.After(until) {
				continue
			}

			if c.CreatedAt.Before(since) {
				continue
			}

			if len(matchUser) > 0 && !matchUser[strings.ToLower(c.Author)] {
				continue
			}

			if c.Author == pr.GetUser().GetLogin() {
				continue
			}

			wordCount := wordCount(c.Body)

			if prMap[c.Author] == nil {
				prMap[c.Author] = &ReviewCommentSummary{
					URL:      pr.GetHTMLURL(),
					PRAuthor: pr.GetUser().GetLogin(),
					Reviewer: c.Author,
					Project:  project,
					Title:    strings.TrimSpace(pr.GetTitle()),
				}
			}
			if prReviewsMap[pr.GetNumber()] == nil {
				prReviewsMap[pr.GetNumber()] = &ReviewSummary{
					URL:       pr.GetHTMLURL(),
					PRAuthor:  pr.GetUser().GetLogin(),
					Project:   project,
					Title:     strings.TrimSpace(pr.GetTitle()),
					Reviewers: []string{c.Author},
				}
				// klog.Infof("ADD %d %d %s", pr.GetNumber(), count2, prReviewsMap[pr.GetNumber()].Reviewers)
			} else {
				if stringInSlice(c.Author, prReviewsMap[pr.GetNumber()].Reviewers) == false {
					prReviewsMap[pr.GetNumber()].Reviewers = append(prReviewsMap[pr.GetNumber()].Reviewers, c.Author)
					// klog.Infof("ADD %d %d %s", pr.GetNumber(), count2, prReviewsMap[pr.GetNumber()].Reviewers)
				}
			}
			if c.Review {
				prMap[c.Author].ReviewComments++
			} else {
				prMap[c.Author].PRComments++
			}

			prReviewsMap[pr.GetNumber()].Date = c.CreatedAt.Format(dateForm)
			prMap[c.Author].Date = c.CreatedAt.Format(dateForm)
			prMap[c.Author].Words += wordCount
			klog.Infof("%d word comment by %s: %q for %s/%s #%d", wordCount, c.Author, strings.TrimSpace(c.Body), org, project, pr.GetNumber())

			// klog.Infof("HELLO %d %s", pr.GetNumber(), prReviewsMap[pr.GetNumber()])
		}

		for _, rs := range prReviewsMap {
			// klog.Infof("HELLO %d %s", pr.GetNumber(), rs.Reviewers)
			if reflect.TypeOf(rs.Reviewers).String() == "string" {
				rs.Reviewer = rs.Reviewers[0]
			} else {
				rs.Reviewer = strings.Join(rs.Reviewers, ",")
			}
			reviews = append(reviews, rs)
		}
	}

	return reviews, err
}

// wordCount counts words in a string, irrespective of language
func wordCount(s string) int {
	// Don't count certain items, like / or - as word segments
	s = notSegmentRe.ReplaceAllString(s, "")

	words := 0
	scanner := bufio.NewScanner(strings.NewReader(s))
	scanner.Split(segment.SplitWords)
	for scanner.Scan() {
		if !unicode.IsLetter(rune(scanner.Bytes()[0])) {
			continue
		}
		words++
	}
	return words
}

func isBot(u *github.User) bool {
	includeBots := viper.GetBool("include-bots")
	if includeBots {
		return false
	}
	if u.GetType() == "bot" {
		return true
	}

	if strings.Contains(u.GetBio(), "stale issues") {
		return true
	}

	if strings.HasSuffix(u.GetLogin(), "bot") {
		return true
	}

	if strings.Contains(u.GetLogin(), "[bot]") {
		return true
	}

	if strings.HasPrefix(u.GetLogin(), "codecov") || strings.HasPrefix(u.GetLogin(), "Travis") {
		return true
	}

	return false
}
