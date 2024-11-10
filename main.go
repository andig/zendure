package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	_ "github.com/joho/godotenv/autoload"
	"github.com/samber/lo"
)

const (
	GridURI = "http://nas.fritz.box:7070/api/state"

	IoBrokerURI = "http://nas.fritz.box:9087"
	DevicePath  = "zendure-solarflow.0.gDa3tb.wywWpySH"

	MaxCharge       = -1200
	ChargeMargin    = 0
	MaxDischarge    = 800
	DischargeMargin = 50
)

const (
	_ = iota
	MODE_INPUT
	MODE_OUTPUT
)

func gridPower() (int64, error) {
	var state struct {
		Result struct {
			GridPower float64 `json:"gridPower"`
		}
	}

	resp, err := http.Get(GridURI)
	if err == nil {
		err = json.NewDecoder(resp.Body).Decode(&state)
	}

	return int64(state.Result.GridPower), err
}

func getSensor(sensor string) (int64, error) {
	uri := fmt.Sprintf("%s/getPlainValue/%s.%s", IoBrokerURI, DevicePath, sensor)
	resp, err := http.Get(uri)
	if err != nil {
		return 0, err
	}

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	return strconv.ParseInt(string(b), 10, 64)
}

type value struct {
	ID  string
	Val int64
}

type sensors []value

func (s sensors) value(sensor string) int64 {
	sensor = fmt.Sprintf("%s.%s", DevicePath, sensor)

	for _, v := range s {
		if v.ID == sensor {
			return v.Val
		}
	}
	return 0
}

func getSensors(sensor ...string) (sensors, error) {
	uri := fmt.Sprintf("%s/getBulk/%s", IoBrokerURI, strings.Join(lo.Map(sensor, func(sensor string, _ int) string {
		return fmt.Sprintf("%s.%s", DevicePath, sensor)
	}), ","))
	resp, err := http.Get(uri)
	if err != nil {
		return nil, err
	}

	var res sensors
	err = json.NewDecoder(resp.Body).Decode(&res)

	return res, err
}

func setControl(cmd string, value int64) {
	fmt.Println(cmd+":", value)

	uri := fmt.Sprintf("%s/set/%s.control.%s?value=%d", IoBrokerURI, DevicePath, cmd, value)
	if _, err := http.Get(uri); err != nil {
		fmt.Println(uri, ":", err)
	}
}

func control() {
	battery := int64(math.MaxInt64)

	minSoc, err := getSensor("minSoc")
	if err != nil {
		panic(err)
	}

	for range time.Tick(10 * time.Second) {
		grid, err := gridPower()
		if err != nil {
			fmt.Println(err)
			continue
		}

		sensors, err := getSensors("acMode", "gridInputPower", "outputHomePower", "electricLevel")
		if err != nil {
			fmt.Println(err)
			continue
		}

		mode := sensors.value("acMode")
		in := sensors.value("gridInputPower")
		out := sensors.value("outputHomePower")
		soc := sensors.value("electricLevel")

		new := grid - in + out
		fmt.Println(new, "=", grid, "grid -", in, "in +", out, "out")

		if new >= 0 {
			new = max(0, min(new-DischargeMargin, MaxDischarge))

			if soc <= minSoc {
				new = 0
			}

			if new != battery {
				if mode != MODE_OUTPUT {
					setControl("acMode", MODE_OUTPUT)
				}

				setControl("setOutputLimit", new)
			}

			if soc <= minSoc {
				fmt.Printf("battery low: %d%%\n", soc)
				time.Sleep(time.Minute)
			}
		} else {
			if new = min(0, max(new+ChargeMargin, MaxCharge)); new != battery {
				if mode != MODE_INPUT {
					setControl("acMode", MODE_INPUT)
				}

				setControl("setInputLimit", -new)
			}
		}

		battery = new
	}
}

func main() {
	go control()

	c := make(chan os.Signal, 1)
	done := make(chan bool, 1)

	signal.Notify(c, os.Interrupt)
	go func() {
		<-c
		close(done)
	}()

	<-done
}
