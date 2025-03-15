module bench-load

go 1.24.1

replace github.com/Jipok/go-persist => ../

require (
	github.com/Jipok/go-persist v1.7.0
	github.com/goccy/go-json v0.10.5
	github.com/tidwall/buntdb v1.3.2
	go.etcd.io/bbolt v1.4.0
)

require (
	github.com/puzpuzpuz/xsync/v3 v3.5.1 // indirect
	github.com/tidwall/btree v1.4.2 // indirect
	github.com/tidwall/gjson v1.14.3 // indirect
	github.com/tidwall/grect v0.1.4 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
	github.com/tidwall/rtred v0.1.2 // indirect
	github.com/tidwall/tinyqueue v0.1.1 // indirect
	golang.org/x/sys v0.29.0 // indirect
)
