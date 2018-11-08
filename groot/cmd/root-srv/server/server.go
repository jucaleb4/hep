// Copyright 2017 The go-hep Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package server encapsulates the creation of the web server for root-srv.
package server // import "go-hep.org/x/hep/groot/cmd/root-srv/server"

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/pborman/uuid"
	"github.com/pkg/errors"
	"go-hep.org/x/hep/groot"
)

const cookieName = "GROOT_SRV"

type server struct {
	mu       sync.RWMutex
	cookies  map[string]*http.Cookie
	sessions map[string]*dbFiles
	local    bool
	dir      string
}

// Init initializes the web server handles.
func Init(local bool) {
	app := newServer(local)
	http.Handle("/", app.wrap(app.rootHandle))
	http.Handle("/root-file-upload", app.wrap(app.uploadHandle))
	http.Handle("/root-file-open", app.wrap(app.openHandle))
	http.Handle("/refresh", app.wrap(app.refreshHandle))
	http.Handle("/plot-h1/", app.wrap(app.plotH1Handle))
	http.Handle("/plot-h2/", app.wrap(app.plotH2Handle))
	http.Handle("/plot-s2/", app.wrap(app.plotS2Handle))
	http.Handle("/plot-branch/", app.wrap(app.plotBranchHandle))
}

func newServer(local bool) *server {
	dir, err := ioutil.TempDir("", "groot-srv-")
	if err != nil {
		dir = "/tmp/groot-srv-tmp"
		log.Printf("could not create temporary directory: %v", err)
		log.Printf("using: %q", dir)
	}

	srv := &server{
		cookies:  make(map[string]*http.Cookie),
		sessions: make(map[string]*dbFiles),
		local:    local,
		dir:      dir,
	}
	go srv.run()
	return srv
}

func (srv *server) run() {
	if srv.dir != "" {
		defer os.RemoveAll(srv.dir)
	}

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		srv.gc()
	}
}

func (srv *server) gc() {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	for name, cookie := range srv.cookies {
		now := time.Now()
		if now.After(cookie.Expires) {
			srv.sessions[name].close()
			delete(srv.sessions, name)
			delete(srv.cookies, name)
			cookie.MaxAge = -1
		}
	}
}

func (srv *server) expired(cookie *http.Cookie) bool {
	now := time.Now()
	return now.After(cookie.Expires)
}

func (srv *server) wrap(fn func(w http.ResponseWriter, r *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := srv.setCookie(w, r)
		if err != nil {
			log.Printf("error retrieving cookie: %v\n", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if err := fn(w, r); err != nil {
			log.Printf("error %q: %v\n", r.URL.Path, err.Error())
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

func (srv *server) setCookie(w http.ResponseWriter, r *http.Request) error {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	cookie, err := r.Cookie(cookieName)
	if err != nil && err != http.ErrNoCookie {
		return err
	}

	if cookie != nil {
		if v, ok := srv.sessions[cookie.Value]; v == nil || !ok {
			srv.sessions[cookie.Value] = newDbFiles()
			srv.cookies[cookie.Value] = cookie
		}
		return nil
	}

	cookie = &http.Cookie{
		Name:    cookieName,
		Value:   uuid.NewRandom().String(),
		Expires: time.Now().Add(24 * time.Hour),
	}
	srv.sessions[cookie.Value] = newDbFiles()
	srv.cookies[cookie.Value] = cookie
	http.SetCookie(w, cookie)
	return nil
}

func (srv *server) cookie(r *http.Request) (*http.Cookie, error) {
	srv.mu.RLock()
	defer srv.mu.RUnlock()
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return nil, err
	}

	if cookie == nil {
		return nil, http.ErrNoCookie
	}
	return srv.cookies[cookie.Value], nil
}

func (srv *server) db(r *http.Request) (*dbFiles, error) {
	srv.mu.RLock()
	defer srv.mu.RUnlock()
	cookie, err := srv.cookie(r)
	if err != nil {
		return nil, err
	}
	return srv.sessions[cookie.Value], nil
}

func (srv *server) rootHandle(w http.ResponseWriter, r *http.Request) error {
	switch r.Method {
	case http.MethodGet:
		// ok
	default:
		return fmt.Errorf("invalid request %q for /", r.Method)
	}

	crutime := time.Now().Unix()
	h := md5.New()
	io.WriteString(h, strconv.FormatInt(crutime, 10))
	token := fmt.Sprintf("%x", h.Sum(nil))

	t, err := template.New("upload").Parse(page)
	if err != nil {
		return err
	}

	return t.Execute(w, struct {
		Token string
		Local bool
	}{token, srv.local})
}

func (srv *server) uploadHandle(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodPost {
		log.Printf("invalid request %q", r.Method)
		return fmt.Errorf("invalid request %q for /root-file-upload", r.Method)
	}

	err := r.ParseMultipartForm(500 << 20)
	if err != nil {
		return errors.Wrapf(err, "could not parse multipart form")
	}

	f, handler, err := r.FormFile("upload-file")
	if err != nil {
		return err
	}
	_, err = f.Seek(0, 0)
	if err != nil {
		return err
	}

	fname := filepath.Join(srv.dir, uuid.NewUUID().String()+".root")
	o, err := os.Create(fname)
	if err != nil {
		return errors.Wrapf(err, "could not create temporary file")
	}
	_, err = io.Copy(o, f)
	if err != nil {
		return errors.Wrapf(err, "could not copy uploaded file")
	}
	o.Close()
	f.Close()

	rfile, err := groot.Open(o.Name())
	if err != nil {
		return err
	}

	db, err := srv.db(r)
	if err != nil {
		return err
	}
	db.set(handler.Filename, rfile)

	var nodes []jsNode

	db.RLock()
	defer db.RUnlock()
	for k, rfile := range db.files {
		node, err := fileJsTree(rfile, k)
		if err != nil {
			return err
		}
		nodes = append(nodes, node...)
	}
	sort.Sort(jsNodes(nodes))
	return json.NewEncoder(w).Encode(nodes)
}

func (srv *server) openHandle(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		return fmt.Errorf("invalid request %q for /root-file-open", r.Method)
	}

	err := r.ParseMultipartForm(500 << 20)
	if err != nil {
		return errors.Wrapf(err, "could not parse multipart form")
	}

	fname := r.FormValue("file-name")
	rfile, err := groot.Open(fname)
	if err != nil {
		return err
	}

	db, err := srv.db(r)
	if err != nil {
		return err
	}
	db.set(fname, rfile)

	var nodes []jsNode

	db.RLock()
	defer db.RUnlock()
	for k, rfile := range db.files {
		node, err := fileJsTree(rfile, k)
		if err != nil {
			return err
		}
		nodes = append(nodes, node...)
	}
	sort.Sort(jsNodes(nodes))
	return json.NewEncoder(w).Encode(nodes)
}

func (srv *server) refreshHandle(w http.ResponseWriter, r *http.Request) error {
	db, err := srv.db(r)
	if err != nil {
		return err
	}

	db.RLock()
	defer db.RUnlock()

	var nodes []jsNode
	for k, rfile := range db.files {
		node, err := fileJsTree(rfile, k)
		if err != nil {
			return err
		}
		nodes = append(nodes, node...)
	}
	sort.Sort(jsNodes(nodes))
	return json.NewEncoder(w).Encode(nodes)
}

const page = `<html>
<head>
    <title>go-hep/groot file inspector</title>
	<meta name="viewport" content="width=device-width, initial-scale=1">
	<link rel="stylesheet" href="https://cdnjs.cloudflare.com/ajax/libs/font-awesome/4.7.0/css/font-awesome.min.css" />
	<link rel="stylesheet" href="https://www.w3schools.com/w3css/3/w3.css">
	<script src="https://ajax.googleapis.com/ajax/libs/jquery/3.1.1/jquery.min.js"></script>
	<link rel="stylesheet" href="//cdnjs.cloudflare.com/ajax/libs/jstree/3.3.7/themes/default/style.min.css" />
	<script src="https://cdnjs.cloudflare.com/ajax/libs/jstree/3.3.7/jstree.min.js"></script>
	<style>
	input[type=file] {
		display: none;
	}
	input[type=submit] {
		background-color: #F44336;
		padding:5px 15px;
		border:0 none;
		cursor:pointer;
		-webkit-border-radius: 5px;
		border-radius: 5px;
	}
	.flex-container {
		display: -webkit-flex;
		display: flex;
	}
	.flex-item {
		margin: 5px;
	}
	.groot-file-upload {
		color: white;
		background-color: #0091EA;
		padding:5px 15px;
		border:0 none;
		cursor:pointer;
		-webkit-border-radius: 5px;
	}
	</style>
</head>
<body>

<!-- Sidebar -->
<div id="groot-sidebar" class="w3-sidebar w3-bar-block w3-card-4 w3-light-grey" style="width:25%">
	<div class="w3-bar-item w3-card-2 w3-black">
		<h2>go-hep/groot ROOT file inspector</h2>
	</div>
	<div class="w3-bar-item">
{{if .Local}}
	<form id="groot-local-form" enctype="multipart/form-data" action="/root-file-open" method="get">
		<input id="groot-file-name" type="text" name="file-name" value>
		<input type="hidden" name="token" value="{{.Token}}"/>
		<input type="hidden" value="upload" />
	</form>
{{- end}}
	<form id="groot-upload-form" enctype="multipart/form-data" action="/root-file-upload" method="post">
		<label for="groot-file-upload" class="groot-file-upload" style="font-size:16px">
{{if .Local}}
		<i class="fa fa-folder-open" aria-hidden="true" style="font-size:16px"></i> Open
{{else}}
		<i class="fa fa-cloud-upload" aria-hidden="true" style="font-size:16px"></i> Upload
{{end}}
		</label>
		<input id="groot-file-upload" type="file" name="upload-file"/>
		<input type="hidden" name="token" value="{{.Token}}"/>
		<input type="hidden" value="upload" />
	</form>

	</div>
	<div id="groot-file-tree" class="w3-bar-item">
	</div>
</div>

<!-- Page Content -->
<div style="margin-left:25%; height:100%" class="w3-grey" id="groot-container">
<div class="w3-container w3-content w3-cell w3-cell-middle w3-cell-row w3-center w3-justify w3-grey" style="width:100%" id="groot-display">
	</div>
</div>

<script type="text/javascript">
	document.getElementById("groot-file-upload").onchange = function() {
		var data = new FormData($("#groot-upload-form")[0]);
		$.ajax({
			url: "/root-file-upload",
			method: "POST",
			data: data,
			processData: false,
			contentType: false,
			success: displayFileTree,
			error: function(er){
				alert("upload failed: "+er);
			}
		});
	}
{{if .Local}}
	document.getElementById("groot-file-name").onchange = function() {
		var data = new FormData($("#groot-local-form")[0]);
		$.ajax({
			url: "/root-file-open",
			method: "POST",
			data: data,
			processData: false,
			contentType: false,
			success: displayFileTree,
			error: function(er){
				alert("open failed: "+er);
			}
		});
	}
{{- end}}
	$(function () {
		$('#groot-file-tree').jstree();
		$("#groot-file-tree").on("select_node.jstree",
			function(evt, data){
				data.instance.toggle_node(data.node);
				if (data.node.a_attr.plot) {
					data.instance.deselect_node(data.node);
					$.get(data.node.a_attr.href, plotCallback);
				}
			}
		);
		$.ajax({
			url: "/refresh",
			method: "GET",
			processData: false,
			contentType: false,
			success: displayFileTree,
			error: function(er){
				alert("refresh failed: "+er);
			}
		});
	});

	function displayFileTree(data) {
		$('#groot-file-tree').jstree(true).settings.core.data = JSON.parse(data);
		$("#groot-file-tree").jstree(true).refresh();
	};

	function plotCallback(data, status) {
		var node = $("<div></div>");
		node.addClass("w3-panel w3-white w3-card-2 w3-display-container w3-content w3-center");
		node.css("width","100%");
		node.html(
			""
			+JSON.parse(data)
			+"<span onclick=\"this.parentElement.style.display='none'; updateHeight();\" class=\"w3-button w3-display-topright w3-hover-red w3-tiny\">X</span>"
		);
		$("#groot-display").prepend(node);
		updateHeight();
	};

	function updateHeight() {
		var hmenu = $("#groot-sidebar").height();
		var hcont = $("#groot-container").height();
		var hdisp = $("#groot-display").height();
		if (hdisp > hcont) {
			$("#groot-container").height(hdisp);
		}
		if (hdisp < hmenu && hcont > hmenu) {
			$("#groot-container").height(hmenu);
		}
	};
</script>
</body>
</html>
`