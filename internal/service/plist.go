package service

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/template"
)

// agentPATH is the PATH the agent's daemon runs with: Homebrew's
// directories on Apple silicon and on Intel, then the system's. launchd's
// own default is /usr/bin:/bin:/usr/sbin:/sbin, where the tools the daemon
// looks for (yt-dlp at startup, node for web2md) aren't. It is fixed rather
// than copied from the installing shell, whose PATH says nothing about the
// login session the agent runs in, and may name a project's own tools.
const agentPATH = "/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:/usr/local/sbin:/usr/bin:/bin:/usr/sbin:/sbin"

// agentPlist is what the agent's plist is rendered from.
type agentPlist struct {
	Label   string
	Program string // the daemon, as installed
	Home    string // CURIO_HOME, set only when NonDefaultHome
	// NonDefaultHome: the home isn't ~/.curio, where the daemon looks
	// without CURIO_HOME.
	NonDefaultHome bool
	StdoutPath     string // the daemon's structured log, daemon.log
	StderrPath     string // launchd.err: the runtime's last words, should it die
}

// plistTemplate is the agent's plist, key by key:
//
//   - RunAtLoad: loading the agent, at install and at each login, starts
//     the daemon.
//   - KeepAlive {SuccessfulExit: false}: launchd restarts a daemon that
//     exits non-zero (a crash, a port it can't bind yet), no more often
//     than its default 10s throttle, and leaves one that exits 0 stopped:
//     one told to stop, one that finds another daemon serving the home,
//     and one refusing a config.yaml or a home it can't serve, which only
//     a fix changes (the daemon's exitCode).
//   - ExitTimeOut: see ExitTimeout.
//   - StandardOutPath is daemon.log, which the daemon logs to through
//     stdout; StandardErrorPath gets only what the runtime writes as the
//     process dies.
//   - EnvironmentVariables: CURIO_HOME for a home other than ~/.curio, and
//     agentPATH. Nothing else of the installing shell's environment is
//     carried over, tokens included: those belong in config.yaml.
//   - No ProcessType: launchd's default, Standard, suits a background
//     service the user waits on.
//
// Every string goes through xml, the only escaping a plist needs.
var plistTemplate = template.Must(template.New("plist").Funcs(template.FuncMap{"xml": xmlText}).Parse(
	`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{xml .Label}}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{xml .Program}}</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ExitTimeOut</key>
	<integer>{{.ExitTimeout}}</integer>
	<key>StandardOutPath</key>
	<string>{{xml .StdoutPath}}</string>
	<key>StandardErrorPath</key>
	<string>{{xml .StderrPath}}</string>
	<key>EnvironmentVariables</key>
	<dict>
{{- if .NonDefaultHome}}
		<key>CURIO_HOME</key>
		<string>{{xml .Home}}</string>
{{- end}}
		<key>PATH</key>
		<string>{{xml .PATH}}</string>
	</dict>
</dict>
</plist>
`))

// renderPlist renders p as the agent's plist.
func renderPlist(p agentPlist) ([]byte, error) {
	var buf bytes.Buffer
	err := plistTemplate.Execute(&buf, struct {
		agentPlist
		ExitTimeout int
		PATH        string
	}{p, int(ExitTimeout.Seconds()), agentPATH})
	if err != nil {
		return nil, fmt.Errorf("render the launchd plist: %w", err)
	}
	return buf.Bytes(), nil
}

// xmlText escapes s for XML character data.
func xmlText(s string) (string, error) {
	var b strings.Builder
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		return "", err
	}
	return b.String(), nil
}

// plistProgram reads the program a plist runs: the first string of its
// ProgramArguments array. It walks the XML tokens rather than decoding the
// plist, which has no schema encoding/xml could decode into; what it
// expects is exactly what renderPlist writes, and a plist edited by hand
// into another shape is an error.
func plistProgram(data []byte) (string, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	var lastKey string
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return "", errors.New("the plist has no ProgramArguments")
		}
		if err != nil {
			return "", fmt.Errorf("read the plist: %w", err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "key":
			var key string
			if err := d.DecodeElement(&key, &start); err != nil {
				return "", fmt.Errorf("read the plist: %w", err)
			}
			lastKey = key
		case "array":
			if lastKey == "ProgramArguments" {
				return firstString(d)
			}
		}
	}
}

// firstString reads the first <string> element of the array d is inside.
func firstString(d *xml.Decoder) (string, error) {
	for {
		tok, err := d.Token()
		if err != nil {
			return "", fmt.Errorf("read the plist's ProgramArguments: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local != "string" {
				return "", fmt.Errorf("the plist's ProgramArguments holds a <%s>, not a <string>", t.Name.Local)
			}
			var program string
			if err := d.DecodeElement(&program, &t); err != nil {
				return "", fmt.Errorf("read the plist's ProgramArguments: %w", err)
			}
			return program, nil
		case xml.EndElement:
			return "", errors.New("the plist's ProgramArguments is empty")
		}
	}
}
