package notify

import (
	"fmt"
	"os/exec"
	"runtime"
)

// Send sends a system notification with the given title and message.
// It uses platform-specific commands:
// - Linux: notify-send
// - macOS: osascript
// - Windows: powershell
func Send(title, message string) error {
	switch runtime.GOOS {
	case "linux":
		return exec.Command("notify-send", title, message).Run()
	case "darwin":
		script := fmt.Sprintf(`display notification %q with title %q`, message, title)
		return exec.Command("osascript", "-e", script).Run()
	case "windows":
		script := fmt.Sprintf(`
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] | Out-Null
$template = @"
<toast>
    <visual>
        <binding template="ToastText02">
            <text id="1">%s</text>
            <text id="2">%s</text>
        </binding>
    </visual>
</toast>
"@
$xml = New-Object Windows.Data.Xml.Dom.XmlDocument
$xml.LoadXml($template)
$toast = [Windows.UI.Notifications.ToastNotification]::new($xml)
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier("gritz").Show($toast)
`, title, message)
		return exec.Command("powershell", "-Command", script).Run()
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}
