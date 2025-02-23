package auth.client

allow if {
	input.header["Authorization"] == "Bearer valid_token"
}
