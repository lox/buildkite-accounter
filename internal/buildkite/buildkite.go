package buildkite

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	errors "golang.org/x/xerrors"
)

const (
	graphQLEndpoint = "https://graphql.buildkite.com/v1"
)

// NewClient returns a new Buildkite GraphQL client
func NewClient(token string) (*Client, error) {
	u, err := url.Parse(graphQLEndpoint)
	if err != nil {
		return nil, errors.Errorf("failed to parse graphql endpoint url: %w", err)
	}
	header := make(http.Header)
	header.Add("Content-Type", "application/json")
	header.Add("Authorization", "Bearer "+token)
	return &Client{
		token:      token,
		endpoint:   u,
		header:     header,
		httpClient: http.DefaultClient,
	}, nil
}

// Client is a Buildkite GraphQL client
type Client struct {
	token      string
	endpoint   *url.URL
	httpClient *http.Client
	header     http.Header
}

// Do sends a GraphQL query with bound variables and returns a Response
func (c *Client) Do(query string, vars map[string]interface{}) (*Response, error) {
	b, err := json.MarshalIndent(struct {
		Query     string                 `json:"query"`
		Variables map[string]interface{} `json:"variables"`
	}{
		Query:     strings.TrimSpace(query),
		Variables: vars,
	}, "", "  ")
	if err != nil {
		return nil, errors.Errorf("failed to marshal vars: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.endpoint.String(), bytes.NewReader(b))
	if err != nil {
		return nil, errors.Errorf("failed to create http request: %w", err)
	}
	req.Header = c.header

	if os.Getenv(`DEBUG`) != "" {
		if dump, err := httputil.DumpRequest(req, true); err == nil {
			fmt.Printf("DEBUG request uri=%s\n%s\n", req.URL, dump)
		}
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, errors.Errorf("request failed: %w", err)
	}

	if os.Getenv(`DEBUG`) != "" {
		if dump, err := httputil.DumpResponse(resp, true); err == nil {
			fmt.Printf("DEBUG response uri=%s\n%s\n", req.URL, dump)
		}
	}

	return &Response{resp}, checkResponseForErrors(resp)
}

// Response is a GraphQL response
type Response struct {
	*http.Response
}

// DecodeInto decodes a JSON body into the provided type
func (r *Response) DecodeInto(v interface{}) error {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return errors.Errorf("error decoding response: %w", err)
	}
	return nil
}

type responseError struct {
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func (r *responseError) Error() string {
	var errors []string
	for _, err := range r.Errors {
		errors = append(errors, err.Message)
	}
	return fmt.Sprintf("graphql error: %s", strings.Join(errors, ", "))
}

func checkResponseForErrors(r *http.Response) error {
	data, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return errors.Errorf("failed to read body: %w", err)
	}

	r.Body.Close()
	r.Body = ioutil.NopCloser(bytes.NewBuffer(data))

	var errResp responseError

	_ = json.Unmarshal(data, &errResp)
	if len(errResp.Errors) > 0 {
		return &errResp
	}

	if r.StatusCode != http.StatusOK {
		return errors.Errorf("response returned status %s", r.Status)
	}

	return nil
}

type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type Authorization struct {
	ID                     string
	Email                  string
	Name                   string
	CreatedAt              time.Time
	ExpireAt               *time.Time
	RevokedAt              *time.Time
	UserSessionDestroyedAt *time.Time
}

type OrgMember struct {
	ID            string
	Name          string
	Email         string
	Role          string
	Bot           bool
	Complimentary bool
	CreatedAt     time.Time
	Authorization *Authorization
}

func (c *Client) getOrgMembersPage(orgSlug string, after string) ([]OrgMember, string, error) {
	resp, err := c.Do(`query ($orgSlug: ID!, $after: String) {
		organization(slug: $orgSlug) {
			members(first: 100, after: $after) {
			  pageInfo {
				hasNextPage
				endCursor
			  }
			  edges {
				node {
				  createdAt
				  role
				  complimentary
				  user {
					id
					email
					name
					bot
				  }
				  sso {
					authorizations(first: 1) {
					  edges {
						node {
						  id
						  identity {
							name
							email
						  }
						  createdAt
						  expiredAt
						  revokedAt
						  userSessionDestroyedAt
						  state
						}
					  }
					}
				  }
				}
			  }
			}
		  }
	  }`, map[string]interface{}{
		`orgSlug`: orgSlug,
		`after`:   after,
	})
	if err != nil {
		return nil, "", errors.Errorf("failed to get authorizations: %w", err)
	}

	var r struct {
		Data struct {
			Organization struct {
				Members struct {
					PageInfo pageInfo `json:"pageInfo"`
					Edges    []struct {
						Node struct {
							CreatedAt     time.Time `json:"createdAt"`
							Role          string    `json:"role"`
							Complimentary bool      `json:"complimentary"`
							User          struct {
								ID    string `json:"id"`
								Name  string `json:"name"`
								Email string `json:"email"`
								Bot   bool   `json:"bot"`
							} `json:"user"`
							Sso struct {
								Authorizations struct {
									Edges []struct {
										Node struct {
											ID       string `json:"id"`
											Identity struct {
												Name  string `json:"name"`
												Email string `json:"email"`
											} `json:"identity"`
											CreatedAt              time.Time  `json:"createdAt"`
											ExpiredAt              *time.Time `json:"expiredAt"`
											RevokedAt              time.Time  `json:"revokedAt"`
											UserSessionDestroyedAt *time.Time `json:"userSessionDestroyedAt"`
										} `json:"node"`
									} `json:"edges"`
								} `json:"authorizations"`
							} `json:"sso"`
						} `json:"node"`
					} `json:"edges"`
				} `json:"members"`
			} `json:"organization"`
		} `json:"data"`
	}

	if err := resp.DecodeInto(&r); err != nil {
		return nil, "", err
	}

	var members []OrgMember

	for _, edge := range r.Data.Organization.Members.Edges {
		member := OrgMember{
			ID:            edge.Node.User.ID,
			Name:          edge.Node.User.Name,
			Email:         edge.Node.User.Email,
			Role:          edge.Node.Role,
			Bot:           edge.Node.User.Bot,
			Complimentary: edge.Node.Complimentary,
			CreatedAt:     edge.Node.CreatedAt,
		}

		if len(edge.Node.Sso.Authorizations.Edges) > 0 {
			authEdge := edge.Node.Sso.Authorizations.Edges[0]

			member.Authorization = &Authorization{
				ID:                     authEdge.Node.ID,
				Email:                  authEdge.Node.Identity.Email,
				Name:                   authEdge.Node.Identity.Name,
				CreatedAt:              authEdge.Node.CreatedAt,
				ExpireAt:               authEdge.Node.ExpiredAt,
				RevokedAt:              &authEdge.Node.RevokedAt,
				UserSessionDestroyedAt: authEdge.Node.UserSessionDestroyedAt,
			}
		}

		members = append(members, member)
	}

	endCursor := r.Data.Organization.Members.PageInfo.EndCursor
	hasNextPage := r.Data.Organization.Members.PageInfo.HasNextPage

	if hasNextPage && endCursor != "" {
		return members, endCursor, nil
	}

	return members, "", nil
}

// GetOrgMembers gets org members and their last authorization
func (c *Client) GetOrgMembers(orgSlug string) ([]OrgMember, error) {
	after := ""
	var result []OrgMember

	for {
		members, nextAfter, err := c.getOrgMembersPage(orgSlug, after)
		if err != nil {
			return nil, err
		}

		result = append(result, members...)

		if nextAfter == "" {
			break
		}

		after = nextAfter
	}

	return result, nil
}
