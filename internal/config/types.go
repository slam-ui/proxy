package config

// Config — структура для парсинга JSON конфига XRay
type Config struct {
	Outbounds []Outbound `json:"outbounds"`
}

type Outbound struct {
	Protocol       string         `json:"protocol"`
	Settings       Settings       `json:"settings"`
	StreamSettings StreamSettings `json:"streamSettings,omitempty"`
}

type Settings struct {
	Vnext []Vnext `json:"vnext"`
}

type Vnext struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
	Users   []User `json:"users"`
}

type User struct {
	ID string `json:"id"`
}

type StreamSettings struct {
	RealitySettings RealitySettings `json:"realitySettings,omitempty"`
}

type RealitySettings struct {
	ServerName string `json:"serverName"`
	PublicKey  string `json:"publicKey"`
	ShortId    string `json:"shortId"`
}
