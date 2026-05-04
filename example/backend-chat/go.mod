module github.com/everfaz/backend-chat

go 1.22

require (
	github.com/everfaz/autobuild-sdk v0.0.0
	github.com/mattn/go-sqlite3 v1.14.24
)

require github.com/alibaba/OpenSandbox/sdks/sandbox/go v1.0.0 // indirect

replace github.com/everfaz/autobuild-sdk => ../../sdk
