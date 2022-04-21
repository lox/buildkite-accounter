package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/hokaccha/go-prettyjson"
	"github.com/lox/buildkite-accounter/internal/buildkite"
)

func main() {
	c := &cli{}
	ctx := kong.Parse(c)
	err := ctx.Run()
	if err != nil && c.Debug {
		ctx.Fatalf("%+v", err)
	}
	ctx.FatalIfErrorf(err)
}

type cli struct {
	Debug    bool     `flag:"" help:"Whether to print debugging"`
	APIToken string   `flag:"" help:"A Buildkite GraphQL Token" env:"BUILDKITE_TOKEN" required:""`
	OrgSlugs []string `flag:"" help:"The buildkite org slug"`
	Cache    bool     `flag:"" help:"Whether to use a disk cache"`
	CacheDir string   `flag:"" help:"The cache directory" type:"path" default:"./.cache"`
	Dedupe   []string `flag:"" help:"Ignore subsequent users" enum:"email,name"`
	Output   string   `flag:"" help:"How to output rows" enum:"count,json,csv" default:"json"`
	Email    string   `flag:"" help:"Filter by email"`
}

type Member struct {
	ID            string     `json:"id"`
	Email         string     `json:"email"`
	Domain        string     `json:"domain"`
	Name          string     `json:"name"`
	Org           string     `json:"org"`
	Role          string     `json:"role"`
	LastAuth      *time.Time `json:"last_auth"`
	Complimentary bool       `json:"complimentary,omitempty"`
}

type MemberWithDuplicates struct {
	Member
	NameDuplicates  []Member `json:"name_duplicates,omitempty"`
	EmailDuplicates []Member `json:"email_duplicates,omitempty"`
}

func (c *cli) Run() error {
	members, err := c.getMembers()
	if err != nil {
		return err
	}

	if c.Debug {
		log.Printf("Found %d accounts over %d accounts", len(members), len(c.OrgSlugs))
	}

	emails := make([]string, 0, len(members))
	for _, member := range members {
		emails = append(emails, member.Email)
	}

	sort.Strings(emails)

	result := []MemberWithDuplicates{}

	// iterate by sorted email
	for _, email := range emails {
		if c.Email != "" && c.Email != email {
			continue
		}
		byEmail := filterMembersByEmail(members, email)
		member := byEmail[0]
		byName := filterMembers(members, func(m Member) bool {
			return m.Name == member.Name && m.Email != member.Email
		})
		result = append(result, MemberWithDuplicates{
			Member:          member,
			EmailDuplicates: byEmail[1:],
			NameDuplicates:  byName,
		})
	}

	var dedupeEmail bool
	var dedupeName bool

	for _, d := range c.Dedupe {
		if d == `email` {
			dedupeEmail = true
		} else if d == `name` {
			dedupeName = true
		}
	}

	// remove duplicates
	if dedupeEmail || dedupeName {
		dupeResults := []MemberWithDuplicates{}
		seenMembers := make(map[string]bool)

		for _, r := range result {
			if _, ok := seenMembers[r.ID]; ok {
				continue
			}
			dupeResults = append(dupeResults, r)
			seenMembers[r.ID] = true

			if dedupeEmail {
				for _, rr := range r.EmailDuplicates {
					seenMembers[rr.ID] = true
				}
			}
			if dedupeName {
				for _, rr := range r.NameDuplicates {
					seenMembers[rr.ID] = true
				}
			}
		}

		result = dupeResults
	}

	if c.Output == `count` {
		fmt.Println(len(result))
	} else if c.Output == `json` {
		s, _ := prettyjson.Marshal(result)
		fmt.Println(string(s))
	} else if c.Output == `csv` {
		csvFile, err := os.Create("output.csv")
		if err != nil {
			return err
		}

		csvWriter := csv.NewWriter(csvFile)
		csvWriter.Write([]string{"email", "name", "org", "role", "last_sso_auth"})

		for _, member := range members {
			lastAuth := ""

			if member.LastAuth != nil {
				lastAuth = member.LastAuth.Format(`2006-01-02 15:04:05`)
			}

			csvWriter.Write([]string{
				member.Email,
				member.Name,
				member.Org,
				member.Role,
				lastAuth,
			})
		}

		csvWriter.Flush()
		csvFile.Close()
	}

	return nil
}

func (c *cli) getMembers() ([]Member, error) {
	client, err := buildkite.NewClient(c.APIToken)
	if err != nil {
		return nil, err
	}

	getOrgMembers := client.GetOrgMembers
	if c.Cache {
		if err := os.MkdirAll(c.CacheDir, 0700); err != nil {
			return nil, err
		}

		// switch in a cached alternative
		getOrgMembers = func(orgSlug string) ([]buildkite.OrgMember, error) {
			cacheFile := filepath.Join(c.CacheDir, orgSlug+".json")

			// serve from cache if it exists
			if _, err := os.Stat(cacheFile); err == nil {
				b, err := ioutil.ReadFile(cacheFile)
				if err != nil {
					return nil, err
				}

				var result []buildkite.OrgMember

				if err := json.Unmarshal(b, &result); err != nil {
					return nil, err
				}

				return result, nil
			}

			// otherwise look up the org members form the API (slow)
			members, err := client.GetOrgMembers(orgSlug)
			if err != nil {
				return nil, err
			}

			b, err := json.Marshal(members)
			if err != nil {
				return nil, err
			}

			// save in cache
			err = ioutil.WriteFile(cacheFile, b, 0600)
			if err != nil {
				return nil, err
			}

			return members, nil
		}
	}

	result := []Member{}
	for _, orgSlug := range c.OrgSlugs {
		if c.Debug {
			log.Printf("Finding members in %s", orgSlug)
		}
		t := time.Now()

		members, err := getOrgMembers(orgSlug)
		if err != nil {
			return nil, err
		}

		for _, orgMember := range members {
			m := Member{
				ID:            orgMember.ID,
				Email:         orgMember.Email,
				Name:          orgMember.Name,
				Org:           orgSlug,
				Role:          strings.ToLower(orgMember.Role),
				Complimentary: orgMember.Complimentary,
			}

			if orgMember.Authorization != nil {
				m.Email = orgMember.Authorization.Email
				m.LastAuth = &orgMember.Authorization.CreatedAt
			}

			domain, err := getEmailDomain(m.Email)
			if err != nil {
				return nil, err
			}
			m.Domain = domain

			result = append(result, m)
		}

		if c.Debug {
			log.Printf("Found %d responses in %v", len(members), time.Since(t))
		}
	}

	return result, nil
}

func filterMembers(members []Member, f func(m Member) bool) (matching []Member) {
	for _, m := range members {
		if f(m) {
			matching = append(matching, m)
		}
	}
	return
}

func filterMembersByEmail(members []Member, email string) (matching []Member) {
	return filterMembers(members, func(m Member) bool {
		return m.Email == email
	})
}

func filterMembersByName(members []Member, name string) (matching []Member) {
	return filterMembers(members, func(m Member) bool {
		return m.Name == name
	})
}

func getEmailDomain(email string) (string, error) {
	at := strings.LastIndex(email, "@")
	if at >= 0 {
		_, domain := email[:at], email[at+1:]
		return domain, nil
	}
	return "", fmt.Errorf("%s is an invalid email address", email)
}
