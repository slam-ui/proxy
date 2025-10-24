package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"proxyclient/internal/api"
	"proxyclient/internal/config"
	"proxyclient/internal/proxy"
	"proxyclient/internal/xray"
	"syscall"
)

const (
	templateFile = "config.template.json"
	secretFile   = "secret.key"
	runtimeFile  = "config.runtime.json"
)

func main() {
	// --- 1. Генерируем конфиг ---
	fmt.Println("Генерируем конфигурацию...")
	err := config.GenerateRuntimeConfig(templateFile, secretFile, runtimeFile)
	if err != nil {
		log.Fatalf("Ошибка при создании конфигурации: %v", err)
	}
	fmt.Println("Конфигурация успешно сгенерирована.")

	// --- 2. Включаем системный прокси ---
	fmt.Println("Включаем системный прокси...")
	err = proxy.SetSystemProxy("127.0.0.1:10807", "<local>")
	if err != nil {
		fmt.Println("Не удалось включить системный прокси:", err)
	} else {
		fmt.Println("Прокси-сервер системы включен: 127.0.0.1:10807")
	}

	// --- 3. Запускаем XRay ---
	fmt.Println("Запускаем XRay...")
	xrayManager, err := xray.NewManager("./xray_core/xray.exe", runtimeFile)
	if err != nil {
		log.Fatal(err)
	}

	// --- 4. Запускаем HTTP API ---
	apiServer := api.NewServer(xrayManager, runtimeFile)
	go func() {
		if err := apiServer.Start(":8080"); err != nil {
			log.Fatal("API сервер упал:", err)
		}
	}()

	// --- 5. Ожидание сигнала завершения ---
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	fmt.Println("\nПолучен сигнал остановки. Отключаем системный прокси...")
	proxy.DisableSystemProxy()

	fmt.Println("Останавливаем XRay...")
	if xrayManager != nil {
		xrayManager.Stop()
	}

	fmt.Println("✅ Работа завершена.")
}
