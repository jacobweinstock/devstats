package github

import "github.com/shurcooL/githubv4"

type pageInfo struct {
	EndCursor   githubv4.String
	HasNextPage githubv4.Boolean
}

type reposQuery struct {
	Organization struct {
		Repositories struct {
			PageInfo pageInfo
			Nodes    []struct {
				Name githubv4.String
			}
		} `graphql:"repositories(first: 100, after: $cursor, orderBy: {field: NAME, direction: ASC})"`
	} `graphql:"organization(login: $org)"`
}

type issuesQuery struct {
	Repository struct {
		Issues struct {
			PageInfo pageInfo
			Nodes    []struct {
				CreatedAt githubv4.DateTime
				Author    struct {
					Login githubv4.String
				}
			}
		} `graphql:"issues(first: 100, after: $cursor, orderBy: {field: CREATED_AT, direction: DESC})"`
	} `graphql:"repository(owner: $org, name: $repo)"`
}

type prsQuery struct {
	Repository struct {
		PullRequests struct {
			PageInfo pageInfo
			Nodes    []struct {
				CreatedAt githubv4.DateTime
				Author    struct {
					Login githubv4.String
				}
			}
		} `graphql:"pullRequests(first: 100, after: $cursor, orderBy: {field: CREATED_AT, direction: DESC})"`
	} `graphql:"repository(owner: $org, name: $repo)"`
}

type reviewsQuery struct {
	Repository struct {
		PullRequests struct {
			PageInfo pageInfo
			Nodes    []struct {
				UpdatedAt githubv4.DateTime
				Reviews   struct {
					Nodes []struct {
						SubmittedAt githubv4.DateTime
						Author      struct {
							Login githubv4.String
						}
					}
				} `graphql:"reviews(first: 100)"`
			}
		} `graphql:"pullRequests(first: 50, after: $cursor, orderBy: {field: UPDATED_AT, direction: DESC})"`
	} `graphql:"repository(owner: $org, name: $repo)"`
}

type commitsQuery struct {
	Repository struct {
		DefaultBranchRef *struct {
			Target struct {
				Commit struct {
					History struct {
						PageInfo pageInfo
						Nodes    []struct {
							CommittedDate githubv4.DateTime
							MessageBody   githubv4.String
							Parents       struct {
								TotalCount githubv4.Int
							}
							Author struct {
								User *struct {
									Login githubv4.String
								}
							}
						}
					} `graphql:"history(first: 100, since: $since, until: $until, after: $cursor)"`
				} `graphql:"... on Commit"`
			}
		}
	} `graphql:"repository(owner: $org, name: $repo)"`
}
