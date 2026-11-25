package web

import (
	"context"
	"html/template"
	"io"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"
	"github.com/tnqbao/kubereel/internal/workflow"
)

type setupViewData struct {
	Username           string
	Message            string
	SetupTokenRequired bool
}

type loginViewData struct {
	Username string
	Next     string
	Message  string
}

type workflowFormViewData struct {
	Username  string
	CSRFToken string
	Message   string
}

type workflowStatusViewData struct {
	Username  string
	CSRFToken string
	Item      workflow.Workflow
}

var (
	setupPageTmpl = template.Must(template.New("setup").Parse(`
<header class="page-header">
	<div>
		<p class="eyebrow">Security</p>
		<h1>Initial setup</h1>
		<p class="page-description">Create the administrator account to unlock the KubeReel dashboard and REST API.</p>
	</div>
</header>
{{if .Message}}<p class="alert-banner" role="alert">{{.Message}}</p>{{end}}
<form class="auth-form" action="/setup" method="post" autocomplete="off">
	<label for="username">Administrator username</label>
	<input id="username" name="username" type="text" minlength="3" maxlength="64" required value="{{.Username}}"/>
	<label for="password">Password (12–72 characters)</label>
	<input id="password" name="password" type="password" minlength="12" maxlength="72" required autocomplete="new-password"/>
	<label for="confirm_password">Confirm password</label>
	<input id="confirm_password" name="confirm_password" type="password" minlength="12" maxlength="72" required autocomplete="new-password"/>
	{{if .SetupTokenRequired}}
	<label for="setup_token">Setup token</label>
	<input id="setup_token" name="setup_token" type="password" required autocomplete="off"/>
	{{end}}
	<button class="mock-button" type="submit">Create administrator</button>
</form>
`))

	loginPageTmpl = template.Must(template.New("login").Parse(`
<header class="page-header">
	<div>
		<p class="eyebrow">Authentication</p>
		<h1>Sign in</h1>
		<p class="page-description">Sign in with your KubeReel administrator account.</p>
	</div>
</header>
{{if .Message}}<p class="alert-banner" role="alert">{{.Message}}</p>{{end}}
<form class="auth-form" action="/login" method="post">
	<input type="hidden" name="next" value="{{.Next}}"/>
	<label for="username">Username</label>
	<input id="username" name="username" type="text" maxlength="64" required autocomplete="username" value="{{.Username}}"/>
	<label for="password">Password</label>
	<input id="password" name="password" type="password" maxlength="72" required autocomplete="current-password"/>
	<button class="mock-button" type="submit">Sign in</button>
</form>
`))

	workflowFormPageTmpl = template.Must(template.New("workflow-form").Parse(`
<header class="page-header">
	<div>
		<p class="eyebrow">Mock provider</p>
		<h1>Mock workflow</h1>
		<p class="page-description">Generate a sample MP4 video without external credentials or AI credits.</p>
	</div>
	{{if .CSRFToken}}
	<form action="/logout" method="post">
		<input type="hidden" name="csrf_token" value="{{.CSRFToken}}"/>
		<button class="logout-button" type="submit">Sign out ({{.Username}})</button>
	</form>
	{{end}}
</header>
{{if .Message}}<p class="alert-banner" role="alert">{{.Message}}</p>{{end}}
<form class="mock-form" action="/workflows" method="post">
	<input type="hidden" name="csrf_token" value="{{.CSRFToken}}"/>
	<label for="prompt">Scene description</label>
	<textarea id="prompt" name="prompt" maxlength="8192" required rows="5"></textarea>
	<button class="mock-button" type="submit">Run mock workflow</button>
</form>
`))

	workflowStatusPageTmpl = template.Must(template.New("workflow-status").Parse(`
<header class="page-header">
	<div>
		<p class="eyebrow">Mock provider</p>
		<h1>Workflow status</h1>
	</div>
	{{if .CSRFToken}}
	<form action="/logout" method="post">
		<input type="hidden" name="csrf_token" value="{{.CSRFToken}}"/>
		<button class="logout-button" type="submit">Sign out ({{.Username}})</button>
	</form>
	{{end}}
</header>
<p><strong>Status:</strong> {{.Item.Status}}</p>
<p><strong>Scene:</strong> {{.Item.Prompt}}</p>
{{if eq .Item.Status "succeeded"}}
<video controls preload="metadata" width="640" src="/api/workflows/{{.Item.ID}}/artifact"></video>
<p><a href="/api/workflows/{{.Item.ID}}/artifact">Download mock MP4</a></p>
{{else if eq .Item.Status "failed"}}
<p class="alert-banner" role="alert">{{.Item.Error}}</p>
{{else}}
<p><a href="/workflows/{{.Item.ID}}">Refresh status</a></p>
{{end}}
<p><a href="/workflows">Start another mock workflow</a></p>
`))
)

func renderWithinLayout(c echo.Context, statusCode int, title string, tmpl *template.Template, data any) error {
	child := templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		return tmpl.Execute(w, data)
	})

	c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	c.Response().WriteHeader(statusCode)
	return Layout(title).Render(templ.WithChildren(c.Request().Context(), child), c.Response())
}
