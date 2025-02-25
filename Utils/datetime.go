package Utils

import "time"

type DateTime struct {
	time.Time
}

const dateTimeFormat = "2006-01-02 15:04:05"

func (d *DateTime) UnmarshalJSON(data []byte) error {
	str := string(data)
	if str == "null" {
		return nil
	}
	str = str[1 : len(str)-1]
	t, err := time.Parse(dateTimeFormat, str)
	if err != nil {
		return err
	}

	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return err
	}
	d.Time = t.In(location)
	return nil
}

func (d DateTime) MarshalJSON() ([]byte, error) {
	if d.IsZero() {
		return []byte("null"), nil
	}
	return []byte(`"` + d.Format(dateTimeFormat) + `"`), nil
}
