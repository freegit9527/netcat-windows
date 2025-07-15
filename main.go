package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"image/color"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

const (
	TCP = "TCP"
	UDP = "UDP"
)

var (
	activeListeners []net.Listener
	activeConns     []net.Conn
	activeUDPSocks  []*net.UDPConn
	mutex           sync.Mutex
)

func main() {
	guiFlag := flag.Bool("gui", false, "启动图形用户界面")
	flag.Parse()

	if *guiFlag {
		startGUI()
	} else {
		startCLI()
	}
}

// CLI 界面
func startCLI() {
	scanner := bufio.NewScanner(os.Stdin)

	for {
		fmt.Println("\n===== 网络工具 =====")
		fmt.Println("1. 发起 TCP 连接")
		fmt.Println("2. 发起 UDP 连接")
		fmt.Println("3. 监听 TCP 端口")
		fmt.Println("4. 监听 UDP 端口")
		fmt.Println("5. 退出")
		fmt.Print("请选择操作: ")

		scanner.Scan()
		choice := scanner.Text()

		switch choice {
		case "1":
			initiateConnection(TCP, scanner)
		case "2":
			initiateConnection(UDP, scanner)
		case "3":
			listenPort(TCP, scanner)
		case "4":
			listenPort(UDP, scanner)
		case "5":
			fmt.Println("退出程序...")
			cleanup()
			return
		default:
			fmt.Println("无效选择，请重试")
		}
	}
}

// 建立连接
func initiateConnection(protocol string, scanner *bufio.Scanner) {
	fmt.Printf("\n===== 发起 %s 连接 =====\n", protocol)
	fmt.Print("目标地址 (IP:端口): ")
	scanner.Scan()
	address := scanner.Text()

	if !validateAddress(address) {
		fmt.Println("无效地址格式，应为 IP:端口")
		return
	}

	if protocol == TCP {
		conn, err := net.Dial("tcp", address)
		if err != nil {
			fmt.Printf("连接失败: %v\n", err)
			return
		}

		mutex.Lock()
		activeConns = append(activeConns, conn)
		mutex.Unlock()

		fmt.Println("连接成功! 输入 'exit' 返回")
		go handleIncoming(conn, protocol)
		handleOutgoing(conn, scanner, protocol)
	} else {
		udpAddr, err := net.ResolveUDPAddr("udp", address)
		if err != nil {
			fmt.Printf("地址解析失败: %v\n", err)
			return
		}

		conn, err := net.DialUDP("udp", nil, udpAddr)
		if err != nil {
			fmt.Printf("连接失败: %v\n", err)
			return
		}

		mutex.Lock()
		activeUDPSocks = append(activeUDPSocks, conn)
		mutex.Unlock()

		fmt.Println("UDP 连接准备就绪! 输入 'exit' 返回")
		go handleIncoming(conn, protocol)
		handleOutgoing(conn, scanner, protocol)
	}
}

// 监听端口
func listenPort(protocol string, scanner *bufio.Scanner) {
	fmt.Printf("\n===== 监听 %s 端口 =====\n", protocol)
	fmt.Print("监听端口: ")
	scanner.Scan()
	port := scanner.Text()

	if !validatePort(port) {
		fmt.Println("无效端口号 (1-65535)")
		return
	}

	address := ":" + port

	if protocol == TCP {
		listener, err := net.Listen("tcp", address)
		if err != nil {
			fmt.Printf("监听失败: %v\n", err)
			return
		}

		mutex.Lock()
		activeListeners = append(activeListeners, listener)
		mutex.Unlock()

		fmt.Printf("正在监听 TCP 端口 %s...\n", port)
		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					if errors.Is(err, net.ErrClosed) {
						return
					}
					fmt.Printf("接受连接失败: %v\n", err)
					continue
				}

				mutex.Lock()
				activeConns = append(activeConns, conn)
				mutex.Unlock()

				fmt.Printf("来自 %s 的新连接\n", conn.RemoteAddr())
				go handleIncoming(conn, protocol)
			}
		}()

	} else {
		udpAddr, err := net.ResolveUDPAddr("udp", address)
		if err != nil {
			fmt.Printf("地址解析失败: %v\n", err)
			return
		}

		conn, err := net.ListenUDP("udp", udpAddr)
		if err != nil {
			fmt.Printf("监听失败: %v\n", err)
			return
		}

		mutex.Lock()
		activeUDPSocks = append(activeUDPSocks, conn)
		mutex.Unlock()

		fmt.Printf("正在监听 UDP 端口 %s...\n", port)
		go handleUDPListener(conn)
	}

	fmt.Print("输入 'exit' 停止监听: ")
	for scanner.Scan() {
		if strings.ToLower(scanner.Text()) == "exit" {
			break
		}
	}

	if protocol == TCP {
		mutex.Lock()
		for _, listener := range activeListeners {
			if listener.Addr().String() == address {
				listener.Close()
			}
		}
		mutex.Unlock()
	} else {
		mutex.Lock()
		for i, conn := range activeUDPSocks {
			if conn.LocalAddr().String() == address {
				conn.Close()
				activeUDPSocks = append(activeUDPSocks[:i], activeUDPSocks[i+1:]...)
				break
			}
		}
		mutex.Unlock()
	}

	fmt.Printf("已停止监听 %s 端口 %s\n", protocol, port)
}

// 处理UDP监听
func handleUDPListener(conn *net.UDPConn) {
	buffer := make([]byte, 4096)
	for {
		n, addr, err := conn.ReadFromUDP(buffer)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			fmt.Printf("UDP 接收错误: %v\n", err)
			continue
		}

		data := buffer[:n]
		now := time.Now().Format("15:04:05")
		fmt.Printf("[%s UDP 来自 %s]: %s\n", now, addr, formatData(data))
	}
}

// 处理传入数据
func handleIncoming(conn net.Conn, protocol string) {
	defer func() {
		mutex.Lock()
		for i, c := range activeConns {
			if c == conn {
				activeConns = append(activeConns[:i], activeConns[i+1:]...)
				break
			}
		}
		mutex.Unlock()
	}()

	buffer := make([]byte, 4096)
	for {
		n, err := conn.Read(buffer)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				fmt.Printf("接收错误: %v\n", err)
			}
			return
		}

		data := buffer[:n]
		now := time.Now().Format("15:04:05")
		fmt.Printf("[%s %s 来自 %s]: %s\n", now, protocol, conn.RemoteAddr(), formatData(data))
	}
}

// 处理传出数据
func handleOutgoing(conn net.Conn, scanner *bufio.Scanner, protocol string) {
	for scanner.Scan() {
		text := scanner.Text()
		if strings.ToLower(text) == "exit" {
			break
		}

		if _, err := conn.Write([]byte(text)); err != nil {
			fmt.Printf("发送失败: %v\n", err)
			break
		}
	}

	if tcpConn, ok := conn.(*net.TCPConn); ok {
		tcpConn.Close()
	} else if udpConn, ok := conn.(*net.UDPConn); ok {
		udpConn.Close()
	}
}

// 格式化数据
func formatData(data []byte) string {
	// 尝试检测是否为纯文本
	if isText(data) {
		return string(data)
	}

	// 否则显示为十六进制
	var buf bytes.Buffer
	for i, b := range data {
		if i > 0 && i%16 == 0 {
			buf.WriteByte('\n')
		} else if i > 0 {
			buf.WriteByte(' ')
		}
		fmt.Fprintf(&buf, "%02X", b)
	}
	return buf.String()
}

// 检查是否为文本数据
func isText(data []byte) bool {
	for _, b := range data {
		if b < 32 && b != '\t' && b != '\n' && b != '\r' {
			return false
		}
	}
	return true
}

// 验证地址格式
func validateAddress(address string) bool {
	_, _, err := net.SplitHostPort(address)
	return err == nil
}

// 验证端口号
func validatePort(port string) bool {
	for _, c := range port {
		if c < '0' || c > '9' {
			return false
		}
	}
	p := 0
	fmt.Sscanf(port, "%d", &p)
	return p > 0 && p <= 65535
}

// 清理资源
func cleanup() {
	mutex.Lock()
	defer mutex.Unlock()

	for _, listener := range activeListeners {
		listener.Close()
	}
	for _, conn := range activeConns {
		conn.Close()
	}
	for _, conn := range activeUDPSocks {
		conn.Close()
	}
}

// ================= GUI 界面 =================

func startGUI() {
	myApp := app.New()
	myWindow := myApp.NewWindow("网络连接工具")
	myWindow.Resize(fyne.NewSize(800, 600))

	// 创建标签页
	tabs := container.NewAppTabs(
		container.NewTabItem("发起连接", makeConnectTab(myWindow)),
		container.NewTabItem("端口监听", makeListenTab(myWindow)),
		container.NewTabItem("关于", makeAboutTab()),
	)

	myWindow.SetContent(tabs)
	myWindow.ShowAndRun()
}

// 创建连接标签页
func makeConnectTab(window fyne.Window) fyne.CanvasObject {
	protocol := widget.NewRadioGroup([]string{TCP, UDP}, func(s string) {})
	protocol.SetSelected(TCP)

	address := widget.NewEntry()
	address.SetPlaceHolder("IP:端口 (例如: 127.0.0.1:8080)")

	// 消息输入框（发送消息）
	message := widget.NewMultiLineEntry()
	message.SetPlaceHolder("输入要发送的消息")
	message.Wrapping = fyne.TextWrapWord
	
	// 创建发送消息的黑色背景容器
	messageBg := canvas.NewRectangle(color.Black)
	messageScroll := container.NewScroll(message)
	messageScroll.SetMinSize(fyne.NewSize(800, 100))
	messageContent := container.NewStack(messageBg, messageScroll)

	// 使用只读的Entry作为接收框
	response := widget.NewMultiLineEntry()
	response.SetPlaceHolder("接收到的消息将显示在这里")
	response.Wrapping = fyne.TextWrapWord
	response.Disable()
	
	// 创建黑色背景容器
	bg := canvas.NewRectangle(color.Black)
	scroll := container.NewScroll(response)
	scroll.SetMinSize(fyne.NewSize(800, 100))
	content := container.NewStack(bg, scroll)

	status := widget.NewLabel("就绪")
	status.Alignment = fyne.TextAlignCenter

	// 连接按钮
	connectBtn := widget.NewButton("连接", func() {
		if address.Text == "" {
			dialog.ShowInformation("错误", "请输入地址", window)
			return
		}

		if !validateAddress(address.Text) {
			dialog.ShowInformation("错误", "无效地址格式", window)
			return
		}

		go func() {
			var conn net.Conn
			var err error

			if protocol.Selected == TCP {
				conn, err = net.Dial("tcp", address.Text)
			} else {
				udpAddr, resolveErr := net.ResolveUDPAddr("udp", address.Text)
				if resolveErr != nil {
					dialog.ShowError(resolveErr, window)
					return
				}
				conn, err = net.DialUDP("udp", nil, udpAddr)
			}

			if err != nil {
				dialog.ShowError(err, window)
				return
			}

			mutex.Lock()
			activeConns = append(activeConns, conn)
			mutex.Unlock()

			status.SetText("已连接到 " + address.Text)

			// 处理传入数据
			go func() {
				buffer := make([]byte, 4096)
				for {
					n, err := conn.Read(buffer)
					if err != nil {
						if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
							status.SetText("接收错误: " + err.Error())
						}
						return
					}

					data := buffer[:n]
					response.SetText(response.Text + fmt.Sprintf("[来自 %s]: %s\n", conn.RemoteAddr(), formatData(data)))
					scroll.ScrollToBottom()
				}
			}()

			// 发送初始消息
			if message.Text != "" {
				if _, err := conn.Write([]byte(message.Text)); err != nil {
					status.SetText("发送失败: " + err.Error())
				}
			}
		}()
	})

	// 发送按钮
	sendBtn := widget.NewButton("发送", func() {
		if message.Text == "" {
			dialog.ShowInformation("错误", "请输入消息", window)
			return
		}

		mutex.Lock()
		defer mutex.Unlock()

		for _, conn := range activeConns {
			if conn.RemoteAddr().String() == address.Text || strings.Contains(conn.RemoteAddr().String(), address.Text) {
				if _, err := conn.Write([]byte(message.Text)); err != nil {
					status.SetText("发送失败: " + err.Error())
					return
				}
				status.SetText("消息已发送")
				return
			}
		}

		status.SetText("没有找到活动连接")
	})

	// 断开按钮
	disconnectBtn := widget.NewButton("断开连接", func() {
		mutex.Lock()
		defer mutex.Unlock()

		for i, conn := range activeConns {
			if conn.RemoteAddr().String() == address.Text || strings.Contains(conn.RemoteAddr().String(), address.Text) {
				conn.Close()
				activeConns = append(activeConns[:i], activeConns[i+1:]...)
				status.SetText("已断开连接")
				return
			}
		}

		status.SetText("没有找到活动连接")
	})

	form := container.NewVBox(
		widget.NewLabel("协议:"),
		protocol,
		widget.NewLabel("目标地址:"),
		address,
		widget.NewLabel("消息:"),
		messageContent,
		container.NewHBox(
			connectBtn,
			sendBtn,
			disconnectBtn,
		),
		widget.NewLabel("响应:"),
		content,
		status,
	)

	return form
}

// 创建监听标签页
func makeListenTab(window fyne.Window) fyne.CanvasObject {
	protocol := widget.NewRadioGroup([]string{TCP, UDP}, func(s string) {})
	protocol.SetSelected(TCP)

	port := widget.NewEntry()
	port.SetPlaceHolder("端口号 (例如: 8080)")

	// 使用只读的Entry作为接收框
	response := widget.NewMultiLineEntry()
	response.SetPlaceHolder("接收到的消息将显示在这里")
	response.Wrapping = fyne.TextWrapWord
	response.Disable()
	
	// 创建黑色背景容器
	bg := canvas.NewRectangle(color.Black)
	scroll := container.NewScroll(response)
	scroll.SetMinSize(fyne.NewSize(800, 300))
	content := container.NewStack(bg, scroll)

	status := widget.NewLabel("就绪")
	status.Alignment = fyne.TextAlignCenter

	// 开始监听按钮
	startBtn := widget.NewButton("开始监听", func() {
		if port.Text == "" {
			dialog.ShowInformation("错误", "请输入端口号", window)
			return
		}

		if !validatePort(port.Text) {
			dialog.ShowInformation("错误", "无效端口号", window)
			return
		}

		address := ":" + port.Text

		go func() {
			if protocol.Selected == TCP {
				listener, err := net.Listen("tcp", address)
				if err != nil {
					dialog.ShowError(err, window)
					return
				}

				mutex.Lock()
				activeListeners = append(activeListeners, listener)
				mutex.Unlock()

				status.SetText("正在监听 TCP 端口 " + port.Text)

				for {
					conn, err := listener.Accept()
					if err != nil {
						if errors.Is(err, net.ErrClosed) {
							return
						}
						status.SetText("接受连接失败: " + err.Error())
						continue
					}

					mutex.Lock()
					activeConns = append(activeConns, conn)
					mutex.Unlock()

					status.SetText("来自 " + conn.RemoteAddr().String() + " 的新连接")

					// 处理传入数据
					go func(conn net.Conn) {
						buffer := make([]byte, 4096)
						for {
							n, err := conn.Read(buffer)
							if err != nil {
								if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
									status.SetText("接收错误: " + err.Error())
								}
								return
							}

							data := buffer[:n]
							now := time.Now().Format("15:04:05")
							response.SetText(response.Text + fmt.Sprintf("[%s 来自 %s]: %s\n", now, conn.RemoteAddr(), formatData(data)))
							scroll.ScrollToBottom()
						}
					}(conn)
				}
			} else {
				udpAddr, err := net.ResolveUDPAddr("udp", address)
				if err != nil {
					dialog.ShowError(err, window)
					return
				}

				conn, err := net.ListenUDP("udp", udpAddr)
				if err != nil {
					dialog.ShowError(err, window)
					return
				}

				mutex.Lock()
				activeUDPSocks = append(activeUDPSocks, conn)
				mutex.Unlock()

				status.SetText("正在监听 UDP 端口 " + port.Text)

				buffer := make([]byte, 4096)
				for {
					n, addr, err := conn.ReadFromUDP(buffer)
					if err != nil {
						if errors.Is(err, net.ErrClosed) {
							return
						}
						status.SetText("接收错误: " + err.Error())
						continue
					}

					data := buffer[:n]
					now := time.Now().Format("15:04:05")
					response.SetText(response.Text + fmt.Sprintf("[%s UDP 来自 %s]: %s\n", now, addr, formatData(data)))
					scroll.ScrollToBottom()
				}
			}
		}()
	})

	// 停止监听按钮
	stopBtn := widget.NewButton("停止监听", func() {
		address := ":" + port.Text

		mutex.Lock()
		defer mutex.Unlock()

		if protocol.Selected == TCP {
			for _, listener := range activeListeners {
				if listener.Addr().String() == address {
					listener.Close()
					status.SetText("已停止监听 TCP 端口 " + port.Text)
					return
				}
			}
		} else {
			for i, conn := range activeUDPSocks {
				if conn.LocalAddr().String() == address {
					conn.Close()
					activeUDPSocks = append(activeUDPSocks[:i], activeUDPSocks[i+1:]...)
					status.SetText("已停止监听 UDP 端口 " + port.Text)
					return
				}
			}
		}

		status.SetText("没有找到活动监听器")
	})

	form := container.NewVBox(
		widget.NewLabel("协议:"),
		protocol,
		widget.NewLabel("监听端口:"),
		port,
		container.NewHBox(
			startBtn,
			stopBtn,
		),
		widget.NewLabel("接收到的消息:"),
		content,
		status,
	)

	return form
}

// 创建关于标签页
func makeAboutTab() fyne.CanvasObject {
	aboutText := `网络连接工具

功能:
- 发起 TCP/UDP 连接
- 监听 TCP/UDP 端口
- 发送和接收文本/二进制数据
- 命令行和图形界面支持

使用说明:
1. 在"发起连接"标签页连接到远程服务器
2. 在"端口监听"标签页监听传入连接
3. 查看接收到的数据

版本: 1.0
作者: 网络工具开发者`

	text := widget.NewLabel(aboutText)
	text.Wrapping = fyne.TextWrapWord
	return container.NewScroll(text)
}

func init() {
	// 设置Ctrl+C信号处理
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cleanup()
		os.Exit(0)
	}()

	// 设置日志输出
	log.SetFlags(0)
	log.SetOutput(new(logWriter))
}

// 自定义日志写入器
type logWriter struct{}

func (writer *logWriter) Write(bytes []byte) (int, error) {
	// 移除时间戳
	if len(bytes) >= 15 && bytes[0] == '2' && bytes[4] == '-' {
		// 跳过时间戳: "2006-01-02T15:04:05Z07:00 "
		// 我们只关心日志消息
		if len(bytes) > 20 {
			fmt.Print(string(bytes[20:]))
			return len(bytes), nil
		}
	}
	fmt.Print(string(bytes))
	return len(bytes), nil
}