# Buildkite Accounter

A tool for enumerating user accounts across multiple Buildkite organizations and finding duplicates either by email or by name.

Output can be presented in JSON or CSV.

## Usage

```
go install github.com/lox/buildkite-accounter
export BUILDKITE_TOKEN=xxx # a graphql token from buildkite.com
buildkite-accounter --org-slugs=my-llama-org,my-alpaca-org --dedupe=email,name

[
  {
    "domain": "llamas.com",
    "email": "llama@llamas.com",
    "email_duplicates": [
      {
        "domain": "llamas.com",
        "email": "llama@llamas.com",
        "id": "xxx==",
        "last_auth": "2022-01-13T06:23:40.362Z",
        "name": "Mr Llama",
        "org": "my-alpaca-org",
        "role": "member"
      }
    ],
    "id": "xxx==",
    "last_auth": "2022-01-25T03:26:39.771Z",
    "name": "Mr Llama",
    "org": "my-llama-org",
    "role": "member"
  }
]
```
