# Policy

## Authentication & Authorization

### Package name

- `auth.client`: Validate request from `backstream` client for connection of WebSocket
- `auth.server`: Validate HTTP request from External Service, Web browser, etc.

### Input

- `method` (string): HTTP method
- `path` (string): HTTP path
- `header` (map of string): HTTP header
- `remote` (string): Remote address

### Output

- `allow` (boolean): Allow or deny the request

### Example

#### Validate fixed token for client request

```rego
package auth.client

allow if {
    input.header.Authorization == "Bearer your_token"
}
```

#### Validate IP address for client request

Allow only requests from specific IP address or IP address range.

```rego
package auth.client

allow if {
    net.cidr_contains("192.0.2.0/24", input.remote)
}
```

#### Validate Google ID Token

Allow only requests from specific email address in Google ID Token.

```rego
package auth.client

jwks_request(url) := http.send({
        "url": url,
        "method": "GET",
        "force_cache": true,
        "force_cache_duration_seconds": 3600,
}).raw_body

verify_google_jwt(header) := claims if {
    authValues := split(header, " ")
    count(authValues) == 2
    lower(authValues[0]) == "bearer"
    token := authValues[1]

    # Get JWKS of google
    jwks := jwks_request("https://www.googleapis.com/oauth2/v3/certs")

    # Verify token
    io.jwt.verify_rs256(token, jwks)
    claims := io.jwt.decode(token)
    print(claims[1])
    claims[1].iss == "https://accounts.google.com"
    time.now_ns() / ((1000 * 1000) * 1000) < claims[1].exp
}

allow if {
    claims := verify_google_jwt(input.header.Authorization)
    claims[1].email == "mizutani@hey.com"
}
```
