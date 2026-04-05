package core

// Services — сервис-локатор приложения.
//
// Агрегирует все зависимости в одной структуре, устраняя необходимость передавать
// каждый сервис отдельным параметром. Компоненты, которым не нужны все зависимости,
// принимают конкретные интерфейсы из core.
//
// Использование:
//
//	// cmd/proxy-client/app.go
//	svc := core.Services{
//	    XRay:         xrayMgr,
//	    Proxy:        proxyMgr,
//	    Routing:      routingEngine,
//	    Log:          appLogger,
//	    Notification: notifSvc,
//	}
//	apiServer := api.NewServer(svc.ToAPIConfig(), ctx)
type Services struct {
	XRay         XRayService
	Proxy        ProxyService
	Routing      RoutingService
	Log          LogService
	Notification NotificationService
	TURN         TURNService
}

// Validate проверяет что обязательные сервисы инициализированы.
// Опциональные: Notification, TURN (могут быть nil).
func (s *Services) Validate() error {
	if s.XRay == nil {
		return errMissing("XRay")
	}
	if s.Proxy == nil {
		return errMissing("Proxy")
	}
	if s.Log == nil {
		return errMissing("Log")
	}
	return nil
}

// errMissing формирует ошибку об отсутствующем сервисе.
func errMissing(name string) error {
	return &MissingServiceError{Name: name}
}

// MissingServiceError возвращается Validate если обязательный сервис не установлен.
type MissingServiceError struct {
	Name string
}

func (e *MissingServiceError) Error() string {
	return "core.Services: обязательный сервис не инициализирован: " + e.Name
}
