package network

// hosts are the hosts on the internet lacia may reach, and what for. A name
// that starts with a dot stands for the hosts under it. GitHub is reached
// by git and curl, which internal/effect/program starts, not from here.
var hosts = []string{
	"discord.com",           // Discord's API: messages, reactions, threads and files
	".discord.gg",           // Discord's gateway, which tells of new messages
	"cdn.discordapp.com",    // images people attach on Discord
	"oauth2.googleapis.com", // Google's tokens, for the calendar
	"www.googleapis.com",    // Google Calendar
}
