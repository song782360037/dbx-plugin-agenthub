module github.com/song782360037/agenthub

go 1.25.0

require (
	github.com/pelletier/go-toml/v2 v2.4.3
	github.com/t8y2/dbx/plugins/sdk/go/dbx-plugin-sdk v0.1.0
	gopkg.in/yaml.v3 v3.0.1
)

// Upstream 948f979869c3087e3257e3d87176c8c8d2b47315; Apache-2.0 in third_party.
replace github.com/t8y2/dbx/plugins/sdk/go/dbx-plugin-sdk => ./third_party/dbx-plugin-sdk
