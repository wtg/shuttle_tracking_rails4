package api

import (
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/wtg/shuttletracker/database"
)

func TestStatic(t *testing.T) {
	type testCase struct {
		method string
		path   string
	}
	cases := []testCase{
		{
			method: "GET",
			path:   "/",
		},
		{
			method: "GET",
			path:   "/static/css/application.css",
		},
		{
			method: "GET",
			path:   "/static/js/frontend.js",
		},
	}

	// Go tests are run from the package dir, but our static files are one level higher
	os.Chdir("..")

	cfg := Config{}
	db := &database.Mock{}

	api, err := New(cfg, db)
	if err != nil {
		t.Errorf("got error '%s', expected nil", err)
		return
	}

	server := httptest.NewServer(api.handler)
	defer server.Close()
	client := server.Client()

	for _, c := range cases {
		url := server.URL + c.path
		req, err := http.NewRequest(c.method, url, nil)
		if err != nil {
			t.Errorf("unable to create HTTP request: %s", err)
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Errorf("unable to do request: %s", err)
			continue
		}

		if resp.StatusCode != 200 {
			t.Logf("%+v", req)
			t.Logf("%+v", resp)
			t.Errorf("%s %s returned status code %d, expected 200", c.method, url, resp.StatusCode)
		}
	}
}

func TestWriteJSON(t *testing.T) {
	type testCase struct {
		data          interface{}
		expectedWrite string
		expectedError bool
	}
	cases := []testCase{
		{
			data:          "hello there",
			expectedWrite: `"hello there"`,
			expectedError: false,
		},
		{
			data: map[int][]string{
				1: []string{
					"hello",
					"there",
				},
			},
			expectedWrite: "{\n" +
				" \"1\": [\n" +
				"  \"hello\",\n" +
				"  \"there\"\n" +
				" ]\n" +
				"}",
			expectedError: false,
		},
		{
			data:          make(chan int),
			expectedWrite: "",
			expectedError: true,
		},
	}

	for _, c := range cases {
		w := httptest.NewRecorder()
		err := WriteJSON(w, c.data)

		if err != nil && !c.expectedError {
			t.Errorf("got error '%s', expected nil", err)
			continue
		} else if c.expectedError && err == nil {
			t.Errorf("didn't get error, expected '%s'", err)
			continue
		} else if c.expectedError && err != nil {
			continue
		}

		res := w.Result()
		if res.Header.Get("Content-Type") != "application/json" {
			t.Errorf("got unexpected Content-Type '%s', expected 'application/json'", res.Header.Get("Content-Type"))
		}

		actualWrite, _ := ioutil.ReadAll(res.Body)
		if string(actualWrite) != c.expectedWrite {
			t.Errorf("got unexpected body '%s', expected '%s'", actualWrite, c.expectedWrite)
		}
	}
}
