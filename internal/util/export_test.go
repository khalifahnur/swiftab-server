package util

import "swiftab/server/internal/config"

func NewMailerForTest() *Mailer {
	return NewMailer(config.SMTPConfig{})
}
