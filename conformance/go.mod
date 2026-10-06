module github.com/vibe-ports/yang/conformance

go 1.26

require go.yaml.in/yaml/v3 v3.0.5

require github.com/vibe-ports/yang v0.0.0

// the engine under test is this repository (design 06 §6)
replace github.com/vibe-ports/yang => ../
