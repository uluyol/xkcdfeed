package app

import (
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io"
	"io/ioutil"
	"net/http"
	"regexp"
	"time"

	"appengine"
	"appengine/memcache"
	"appengine/urlfetch"
)

type Link struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr,omitempty"`
}

type Feed struct {
	XMLName xml.Name `xml:"http://www.w3.org/2005/Atom feed"`
	Lang    string   `xml:"http://www.w3.org/XML/1998/namespace lang,attr,omitempty"`
	Title   string   `xml:"title"`
	Link    []Link   `xml:"link"`
	ID      string   `xml:"id"`
	Updated string   `xml:"updated"`
	Entry   []Entry  `xml:"entry"`
}

type Entry struct {
	Title   string `xml:"title"`
	Link    []Link `xml:"link"`
	Updated string `xml:"updated"`
	ID      string `xml:"id"`
	Summary struct {
		Type string `xml:"type,attr,omitempty"`
		Body string `xml:",innerxml"`
	} `xml:"summary"`
}

var (
	altExp           = regexp.MustCompile(`alt="[^"]*"`)
	httpExp          = regexp.MustCompile(`http://(imgs\.)?xkcd.com`)
	httpsReplacement = []byte("https://${1}xkcd.com")
)

func (e *Entry) AltText() string {
	s := altExp.FindString(e.Summary.Body)
	if s == "" {
		return s
	}
	return s[5 : len(s)-1]
}

func init() {
	http.HandleFunc("/atom.xml", atomHandler)
	http.HandleFunc("/", mainHandler)
}

func getUpstreamAtom(ctx appengine.Context) (*Feed, error) {
	client := urlfetch.Client(ctx)
	resp, err := client.Get("https://xkcd.com/atom.xml")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("http request was not OK")
	}
	defer resp.Body.Close()
	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %v", err)
	}
	b = httpExp.ReplaceAll(b, httpsReplacement)
	var feed Feed
	if err := xml.Unmarshal(b, &feed); err != nil {
		return nil, fmt.Errorf("failed to unmarshal feed: %v", err)
	}
	return &feed, nil
}

const atomKey = "/xkcd.atom"

func cachingGetUpstreamAtom(ctx appengine.Context) (*Feed, error) {
	item, err := memcache.Get(ctx, atomKey)
	if err != nil {
		ctx.Infof("making request to xkcd.com")
		feed, err := getUpstreamAtom(ctx)
		if err != nil {
			return nil, err
		}
		if b, err := xml.Marshal(feed); err == nil {
			item = &memcache.Item{
				Key:        atomKey,
				Value:      b,
				Expiration: 5 * time.Minute,
			}
			memcache.Set(ctx, item)
		}
		return feed, nil
	}
	ctx.Infof("found feed in cache")
	var feed Feed
	if err := xml.Unmarshal(item.Value, &feed); err != nil {
		return nil, fmt.Errorf("failed to unmarshal cached feed: %v", err)
	}
	return &feed, nil
}

func atomHandler(w http.ResponseWriter, r *http.Request) {
	ctx := appengine.NewContext(r)
	feed, err := cachingGetUpstreamAtom(ctx)
	if err != nil {
		http.Error(w, "failed to get upstream atom: "+err.Error(), http.StatusInternalServerError)
		return
	}
	for i := range feed.Entry {
		feed.Entry[i].Summary.Body += "\n" + feed.Entry[i].AltText()
	}
	b, err := xml.Marshal(feed)
	if err != nil {
		http.Error(w, "failed to marshal feed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/atom+xml")
	io.WriteString(w, xml.Header)
	w.Write(b)
}

const mainRawTemplate = `<!doctype html>
<html>
<title>xkcd with subs</title>
<link rel="stylesheet" type="text/css" href="static/style.css">
<link type="application/atom+xml" rel="alternate" href="/atom.xml"/>
</html>
<body>
<h1>xkcd with captions</h1>
<p class="desc">
I am a fan of Randall Munroe's xkcd comic. I read it on a regular basis and
enjoy the mouseover texts that come with the comics, but those can't easily
be read on mobile devices. So this website has an <a href="/atom.xml">RSS
feed</a> which pulls out the text and places it under the image. The feed
links back to <a href="https://xkcd.com">xkcd.com</a>. Enjoy. This page only
shows the latest entries.
</p>
{{range .}}
<div class="entry">
<h2>{{.Title}}</h2>
{{.Img}}
<p class="caption">{{.Text}}</p>
</div>
{{end}}
</body>
`

var mainTemplate = template.Must(template.New("main").Parse(mainRawTemplate))

type pageEntry struct {
	Title string
	Img   template.HTML
	Text  template.HTML
}

func mainHandler(w http.ResponseWriter, r *http.Request) {
	ctx := appengine.NewContext(r)
	feed, err := cachingGetUpstreamAtom(ctx)
	if err != nil {
		http.Error(w, "failed to get upstream atom: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var entries []pageEntry
	for i := range feed.Entry {
		imgTag := html.UnescapeString(feed.Entry[i].Summary.Body)
		text := html.UnescapeString(feed.Entry[i].AltText())
		entries = append(entries, pageEntry{
			Title: feed.Entry[i].Title,
			Img:   template.HTML(imgTag),
			Text:  template.HTML(text),
		})
	}
	if err := mainTemplate.Execute(w, entries); err != nil {
		http.Error(w, "failed to execute template", http.StatusInternalServerError)
	}
}
