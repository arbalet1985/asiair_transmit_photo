package gui

import "github.com/lxn/walk"

// logModel feeds the log list box: one item per line, newest last.
type logModel struct {
	walk.ListModelBase
	items []string
}

const logModelMax = 1000

func (m *logModel) ItemCount() int              { return len(m.items) }
func (m *logModel) Value(index int) interface{} { return m.items[index] }

func (m *logModel) add(line string) {
	if len(m.items) >= logModelMax {
		m.items = append([]string(nil), m.items[len(m.items)-logModelMax/2:]...)
		m.items = append(m.items, line)
		m.PublishItemsReset()
		return
	}
	m.items = append(m.items, line)
	m.PublishItemsInserted(len(m.items)-1, len(m.items)-1)
}
