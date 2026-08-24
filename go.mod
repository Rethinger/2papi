module github.com/Rethinger/2papi

go 1.22

require (
	github.com/Rethinger/squoze v0.1.1
	golang.org/x/net v0.33.0
	gopkg.in/yaml.v3 v3.0.1
)

replace github.com/Rethinger/squoze => ../squoze

require (
	github.com/grandcat/zeroconf v1.0.0
	golang.org/x/term v0.27.0
)

require (
	github.com/cenkalti/backoff v2.2.1+incompatible // indirect
	github.com/miekg/dns v1.1.41 // indirect
	github.com/tidwall/gjson v1.17.3 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	golang.org/x/sys v0.28.0 // indirect
)
