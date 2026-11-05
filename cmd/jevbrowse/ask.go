package main

// Asking Jev what to do next. Jev picks from options: the action (click, type, back, done), the item to click or the text field to
// fill, which of the allowed texts to type, and whether to press Enter afterwards. It never writes text itself: only texts the user
// listed on the form can be typed.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type decision struct {
	Act   string // click, type, back, done
	Idx   int    // the item to click or the field to fill
	Text  string // what to type
	Enter bool   // press Enter after typing
	Conf  float64
}

// markJS draws a red box and the item number over every link, button and field in view, so that a screenshot matches the item list.
const markJS = `(function(){var o=document.createElement('div');o.id='__jbm';o.style.cssText='position:fixed;left:0;top:0;width:100vw;height:100vh;z-index:2147483647;pointer-events:none';
[].slice.call(document.querySelectorAll('[data-jb]')).forEach(function(e){var r=e.getBoundingClientRect();if(r.bottom<0||r.top>innerHeight||r.right<0||r.left>innerWidth)return;
var b=document.createElement('div');b.style.cssText='position:absolute;left:'+r.left+'px;top:'+r.top+'px;width:'+r.width+'px;height:'+r.height+'px;outline:2px solid #e5484d;box-sizing:border-box';
var n=document.createElement('span');n.textContent=e.getAttribute('data-jb');n.style.cssText='position:absolute;left:0;top:0;background:#e5484d;color:#fff;font:700 18px/20px sans-serif;padding:0 4px';b.appendChild(n);o.appendChild(b)});
document.documentElement.appendChild(o)})()`

const unmarkJS = `(function(){var o=document.getElementById('__jbm');if(o)o.remove()})()`

// markedShot returns a base64 JPEG of the page as it is on screen, with the numbered boxes drawn on ("" if it fails).
func markedShot(c *cdpConn) string {
	eval(c, markJS)
	time.Sleep(120 * time.Millisecond)
	raw, err := c.call("Page.captureScreenshot", map[string]any{"format": "jpeg", "quality": 50, "clip": map[string]any{"x": 0, "y": 0, "width": 1280, "height": 720, "scale": 0.6}}) // smaller picture = fewer tokens = faster
	eval(c, unmarkJS)
	if err != nil {
		return ""
	}
	var r struct {
		Data string `json:"data"`
	}
	json.Unmarshal(raw, &r)
	return r.Data
}

func askJev(task string, p pageInfo, els []element, hist []string, texts []string, pic string) (decision, error) {
	d := decision{Idx: -1}
	var fields, clicks []element
	for _, e := range els {
		if e.K == "t" {
			fields = append(fields, e)
		} else {
			clicks = append(clicks, e)
		}
	}
	canType := len(fields) > 0 && len(texts) > 0

	var st strings.Builder
	fmt.Fprintf(&st, "Task: %s\nCurrent page: %s (%s)\nPage text: %s\n", task, p.Title, p.URL, p.Text)
	if len(hist) > 0 {
		st.WriteString("Steps so far:\n")
		for _, h := range hist {
			st.WriteString("- " + h + "\n")
		}
	}
	if canType {
		st.WriteString("Texts the browser may type (nothing else):\n")
		for i, t := range texts {
			fmt.Fprintf(&st, "<%d> %s\n", i, t)
		}
	}
	st.WriteString("Items on the page:\n")
	for _, e := range els {
		if e.K == "t" {
			fmt.Fprintf(&st, "[%d] text field: %s", e.I, e.T)
			if e.V != "" {
				fmt.Fprintf(&st, " (already contains: %s)", e.V)
			}
			st.WriteString("\n")
		} else {
			fmt.Fprintf(&st, "[%d] %s %s\n", e.I, e.T, short(e.H))
		}
	}

	crit := map[string]string{
		"done": "The current page already completes or answers the task",
		"back": "Go back to the previous page because this page is not useful",
	}
	qs := map[string]any{}
	if len(clicks) > 0 {
		crit["click"] = "Click one of the links or buttons to get closer to the task"
		ec := map[string]string{}
		for _, e := range clicks {
			ec["e"+strconv.Itoa(e.I)] = fmt.Sprintf("[%d] %s", e.I, e.T)
		}
		qs["q2"] = map[string]any{"type": "choice", "instructions": "If the browser clicks, which item brings it closest to completing the task?", "criteria": ec}
	}
	if len(fields) > 0 {
		crit["write"] = "Write text into one of the text fields by pressing keys one at a time (the task says what to write)"
		fc := map[string]string{}
		for _, e := range fields {
			fc["e"+strconv.Itoa(e.I)] = fmt.Sprintf("[%d] text field: %s", e.I, e.T)
		}
		qs["q3"] = map[string]any{"type": "choice", "instructions": "If the browser types, into which text field?", "criteria": fc}
	}
	if canType {
		crit["type"] = "Type one of the allowed texts into one of the text fields in one go"
		tc := map[string]string{}
		for i, t := range texts {
			tc["t"+strconv.Itoa(i)] = fmt.Sprintf("<%d> %s", i, t)
		}
		qs["q4"] = map[string]any{"type": "choice", "instructions": "If the browser types, which text?", "criteria": tc}
		qs["q5"] = map[string]any{"type": "choice", "instructions": "After typing, should the browser press Enter to send the text?", "criteria": map[string]string{
			"enter": "Press Enter (for a search box or a form with one field)",
			"stay":  "Do not press Enter; more fields or a button come next",
		}}
	}
	qs["q1"] = map[string]any{"type": "choice", "instructions": "What should the browser do next to complete the task?", "criteria": crit}

	var state any = st.String()
	if pic != "" {
		st.WriteString("A picture of the page follows. The red boxes on it carry the same numbers as the items above.\n")
		state = []any{st.String(), map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:image/jpeg;base64," + pic}}}
	}
	body, _ := json.Marshal(map[string]any{"model": jevModel, "state": state, "questions": qs})
	var last error
	for try := 0; try < 2; try++ {
		req, _ := http.NewRequest("POST", jevURL, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+jevKey())
		req.Header.Set("Content-Type", "application/json")
		if pic != "" {
			req.Header.Set("TypeSafe-Danger-Zone-Images", "true")
			req.Header.Set("TypeSafe-Danger-Zone-Long-Context", "true") // a page, its item list and a screenshot together are too many tokens for the default
		}
		resp, err := (&http.Client{Timeout: 40 * time.Second}).Do(req)
		if err != nil {
			last = err
			continue
		}
		var out struct {
			Answers map[string]struct {
				Choice string             `json:"choice"`
				Probs  map[string]float64 `json:"probabilities"`
			} `json:"answers"`
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		err = json.Unmarshal(raw, &out)
		if resp.StatusCode != 200 || err != nil {
			last = fmt.Errorf("Jev answered %d: %s", resp.StatusCode, clip(string(raw), 400))
			continue
		}
		a := out.Answers["q1"]
		d.Act, d.Conf = a.Choice, a.Probs[a.Choice]
		num := func(q, prefix string) int {
			n, err := strconv.Atoi(strings.TrimPrefix(out.Answers[q].Choice, prefix))
			if err != nil {
				return -1
			}
			return n
		}
		switch d.Act {
		case "click":
			d.Idx = num("q2", "e")
		case "write":
			d.Idx = num("q3", "e")
		case "type":
			d.Idx = num("q3", "e")
			if t := num("q4", "t"); t >= 0 && t < len(texts) {
				d.Text = texts[t]
			}
			d.Enter = out.Answers["q5"].Choice == "enter"
		}
		return d, nil
	}
	return d, last
}

// focusField puts the cursor in the field and selects what is in it, so the next typing replaces it.
func focusField(c *cdpConn, idx int) {
	eval(c, fmt.Sprintf(`(function(){var e=document.querySelector('[data-jb="%d"]');if(e){e.scrollIntoView({block:'center'});e.focus();if(e.select)e.select()}})()`, idx))
}

// pressKey sends a real key press (Enter or Backspace) to the page.
func pressKey(c *cdpConn, key string) {
	code, vk, text := "Enter", 13, "\r"
	if key == "Backspace" {
		code, vk, text = "Backspace", 8, ""
	}
	for _, t := range []string{"keyDown", "keyUp"} {
		ev := map[string]any{"type": t, "key": key, "code": code, "windowsVirtualKeyCode": vk, "nativeVirtualKeyCode": vk}
		if t == "keyDown" && text != "" {
			ev["text"] = text
		}
		c.call("Input.dispatchKeyEvent", ev)
	}
}

// typeInto fills the field with the text (replacing what is in it) and optionally presses Enter.
func typeInto(c *cdpConn, idx int, text string, enter bool) {
	focusField(c, idx)
	c.call("Input.insertText", map[string]any{"text": text})
	if enter {
		time.Sleep(200 * time.Millisecond)
		pressKey(c, "Enter")
	}
}

// ---- the keyboard: Jev spells text one key at a time ----

var keyDesc = map[string]string{} // key name -> what Jev is told
var keyChar = map[string]string{} // key name -> the character it types

func init() {
	for c := 'a'; c <= 'z'; c++ {
		keyDesc[string(c)], keyChar[string(c)] = "the letter "+string(c), string(c)
	}
	for c := '0'; c <= '9'; c++ {
		k := "d" + string(c)
		keyDesc[k], keyChar[k] = "the digit "+string(c), string(c)
	}
	sym := map[string]string{"excl": "!", "at": "@", "hash": "#", "dollar": "$", "percent": "%", "caret": "^", "amp": "&", "star": "*",
		"lparen": "(", "rparen": ")", "dot": ".", "dash": "-", "underscore": "_", "slash": "/", "colon": ":", "question": "?", "plus": "+"}
	for k, c := range sym {
		keyDesc[k], keyChar[k] = "the symbol "+c, c
	}
	keyDesc["space"], keyChar["space"] = "a space", " "
	keyDesc["backspace"] = "Backspace: delete the last typed character"
	keyDesc["enter"] = "Enter: the text is complete, press Enter to send it"
	keyDesc["stop"] = "Stop: the text is complete, do not press Enter"
}

// askKey asks Jev for the next key.
func askKey(task, field, typed string) (string, error) {
	shown := typed
	if shown == "" {
		shown = "(nothing yet)"
	}
	state := fmt.Sprintf("Task: %s\nThe browser is typing into the text field %q one key at a time.\nTyped so far: %s\n", task, field, shown)
	body, _ := json.Marshal(map[string]any{"model": jevModel, "state": state, "questions": map[string]any{
		"q1": map[string]any{"type": "choice", "instructions": "Which key comes next? Spell exactly the text the task asks for; when it is complete choose Enter or Stop.", "criteria": keyDesc},
	}})
	var last error
	for try := 0; try < 2; try++ {
		req, _ := http.NewRequest("POST", jevURL, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+jevKey())
		req.Header.Set("Content-Type", "application/json")
		resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			last = err
			continue
		}
		var out struct {
			Answers map[string]struct {
				Choice string `json:"choice"`
			} `json:"answers"`
		}
		err = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if resp.StatusCode != 200 || err != nil {
			last = fmt.Errorf("Jev answered %d", resp.StatusCode)
			continue
		}
		return out.Answers["q1"].Choice, nil
	}
	return "", last
}

// writeKeys lets Jev spell text into the field, at most 60 keys. It returns what was typed and whether Enter was pressed.
func writeKeys(j *job, c *cdpConn, task string, idx int, field string) (string, bool) {
	focusField(c, idx)
	pressKey(c, "Backspace") // clears what was selected
	typed := ""
	for i := 0; i < 60; i++ {
		k, err := askKey(task, field, typed)
		if err != nil {
			break
		}
		switch k {
		case "enter":
			time.Sleep(200 * time.Millisecond)
			pressKey(c, "Enter")
			return typed, true
		case "stop", "":
			return typed, false
		case "backspace":
			if r := []rune(typed); len(r) > 0 {
				typed = string(r[:len(r)-1])
				pressKey(c, "Backspace")
			}
		default:
			if ch, ok := keyChar[k]; ok {
				typed += ch
				c.call("Input.insertText", map[string]any{"text": ch})
			}
		}
		j.setNow(fmt.Sprintf("Writing into %q: %s", field, typed))
	}
	return typed, false
}
