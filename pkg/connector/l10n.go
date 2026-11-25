package connector

import (
	"go.mau.fi/util/l10n"
)

var L = l10n.NewNamespace("mautrix-discord")

var (
	msgFlowBrowserName        = L.Define("login.flow.token.name", "Browser")
	msgFlowBrowserDescription = L.Define("login.flow.token.description", "Log in to your Discord account in a web browser.")
	msgFlowQRName             = L.Define("login.flow.qr.name", "QR Code")
	msgFlowQRDescription      = L.Define("login.flow.qr.description", "Scan a QR code with the Discord mobile app to log in.")
	msgFlowTokenName          = L.Define("login.flow.debug_userinput_token.name", "Token")
	msgFlowTokenDescription   = L.Define("login.flow.debug_userinput_token.description", "Provide a Discord user token to connect with.")
	msgFlowMachineName        = L.Define("login.flow.machine.name", "Email/Phone & Password")
	msgFlowMachineDescription = L.Define("login.flow.machine.description", "Log in with an email or phone number and a password. Supports multi-factor authentication (e.g. TOTP, SMS, etc.)")

	msgFieldUsernameOrPhoneName          = L.Define("login.field.username_or_phone.name", "Email or phone number")
	msgFieldPasswordName                 = L.Define("login.field.password.name", "Password")
	msgFieldEmailVerificationName        = L.Define("login.field.email_verification.name", "Verification")
	msgFieldEmailVerificationOptVerified = L.Define("login.field.email_verification.option.verified", "I’ve verified the login")
	msgFieldSMSCodeName                  = L.Define("login.field.sms_code.name", "Verification code")
	msgFieldSMSCodeDescription           = L.Define("login.field.sms_code.description", "The code might take a moment to arrive.")
	msgFieldMFAMethodName                = L.Define("login.field.mfa_method.name", "Verification Method")
	msgFieldMFAMethodOptSMS              = L.Define("login.field.mfa_method.option.sms", "Text me a code")
	msgFieldMFAMethodOptTOTP             = L.Define("login.field.mfa_method.option.totp", "Use my authenticator app")
	msgFieldMFAMethodOptBackup           = L.Define("login.field.mfa_method.option.backup", "Enter a backup code")
	msgFieldMFABackupCodeName            = L.Define("login.field.mfa_backup_code.name", "Backup code")
	msgFieldMFABackupCodeDescription     = L.Define("login.field.mfa_backup_code.description", "You won’t be able to use this backup code again.")
	msgFieldMFATOTPCodeName              = L.Define("login.field.mfa_totp_code.name", "Authentication code")
	msgFieldTokenName                    = L.Define("login.field.token.name", "Discord user account token")

	msgStepCookiesInstructions                = L.Define("login.step.cookies.instructions", "Log in with Discord.")
	msgStepQRInstructions                     = L.Define("login.step.qr.instructions", "On your phone, find “Scan QR Code” in Discord’s settings.")
	msgStepCredsInvalidInstructions           = L.Define("login.step.creds.invalid.instructions", "Invalid email/phone number or password.")
	msgStepEmailVerificationInstructions      = L.Define("login.step.email_verification.instructions", "Your login was correct, but Discord detected Beeper as a new login location. Check your email for a verification link, then choose the option below to continue.")
	msgStepEmailVerificationRetryInstructions = L.Define("login.step.email_verification.retry.instructions", "Discord hasn’t confirmed this login yet. Open the newest email from Discord and tap Verify Login, then choose the option below to continue.")
	msgStepSMSVerificationInstructions        = L.Define("login.step.sms_verification.instructions", "Your login was correct, but Discord detected Beeper as a new login location. Enter the code Discord just texted you.")
	msgStepSMSVerificationRetryInstructions   = L.Define("login.step.sms_verification.retry.instructions", "That code didn’t work. Check your information and try again.")
	msgStepMFAMethodInstructions              = L.Define("login.step.mfa.method.instructions", "How do you want to verify it’s you?")
	msgStepMFAMethodRetryInstructions         = L.Define("login.step.mfa.method.retry.instructions", "That code didn’t work. Choose how you’d like to verify and try again.")
	msgStepMFAMethodRetryBackupInstructions   = L.Define("login.step.mfa.method.retry_backup.instructions", "That code didn’t work. Choose how you’d like to verify and try again. If your authenticator app isn’t working, you can use a backup code instead.")
	msgStepMFASMSInstructions                 = L.Define("login.step.mfa.sms.instructions", "Enter the code Discord just texted you.")
	msgStepMFATOTPInstructions                = L.Define("login.step.mfa.totp.instructions", "Enter the code from your authenticator app.")
	msgStepMFABackupInstructions              = L.Define("login.step.mfa.backup.instructions", "If your authenticator app is unavailable, you can sign in with a backup code. Backup codes are meant for emergencies only.")
	msgStepCaptchaInstructions                = L.Define("login.step.captcha.instructions", "Discord is presenting a CAPTCHA challenge.")
	msgStepCompleteInstructions               = L.Define(
		"login.step.complete.instructions", "Logged in as {username}",
		l10n.WithComment("username is the Discord username of the account"),
	)
)
