package mailer

import "log/slog"

// LogMailer is the dev-mode Mailer used when SMTP is unconfigured: it logs
// the full message (so verification links can be picked from logs) and always
// succeeds. Never used in prod — prod validation requires SMTP.
type LogMailer struct {
	log *slog.Logger
}

// NewLogMailer builds a LogMailer.
func NewLogMailer(log *slog.Logger) *LogMailer {
	if log == nil {
		log = slog.Default()
	}
	return &LogMailer{log: log}
}

// Send implements Mailer.
func (m *LogMailer) Send(to, subject, html, text string, headers map[string]string) error {
	m.log.Info("dev mailer: email logged instead of sent",
		"to", to, "subject", subject, "text", text, "headers", headers)
	return nil
}
