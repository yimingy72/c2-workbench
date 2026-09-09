import { useEffect, useState } from 'react'
import { Button, Drawer, Form, Input, InputNumber, Space, Table, Typography, Popconfirm, App } from 'antd'
import { api } from '../api'

type Listener = { id: string; name: string; bind: string; port: number }

export default function Listeners() {
  const { message } = App.useApp()
  const [rows, setRows] = useState<Listener[]>([])
  const [open, setOpen] = useState(false)
  const [loading, setLoading] = useState(false)

  const load = async () => {
    setLoading(true)
    try {
      const data = (await api('/api/listeners')) as Listener[]
      setRows(Array.isArray(data) ? data : [])
    } catch (e) {
      message.error(e instanceof Error ? e.message : '加载失败')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
  }, [])

  return (
    <div>
      <Space style={{ marginBottom: 16 }}>
        <Typography.Title level={4} style={{ margin: 0 }}>
          监听管理
        </Typography.Title>
        <Button type="primary" onClick={() => setOpen(true)}>
          新增监听
        </Button>
      </Space>
      <Table
        rowKey="id"
        loading={loading}
        dataSource={rows}
        pagination={false}
        columns={[
          { title: 'ID', dataIndex: 'id' },
          { title: '名称', dataIndex: 'name' },
          { title: '绑定', dataIndex: 'bind' },
          { title: '端口', dataIndex: 'port' },
          {
            title: '操作',
            render: (_, r) => (
              <Popconfirm
                title="确认删除该监听？"
                onConfirm={async () => {
                  try {
                    await api(`/api/listeners/${r.id}`, { method: 'DELETE' })
                    message.success('已删除')
                    await load()
                  } catch (e) {
                    message.error(e instanceof Error ? e.message : '删除失败')
                  }
                }}
              >
                <Button danger size="small">
                  删除
                </Button>
              </Popconfirm>
            ),
          },
        ]}
      />
      <Drawer title="新增监听" open={open} onClose={() => setOpen(false)} destroyOnHidden>
        <Form
          layout="vertical"
          initialValues={{ bind: '0.0.0.0', port: 34567 }}
          onFinish={async (v: { name: string; bind: string; port: number }) => {
            try {
              await api('/api/listeners', { method: 'POST', body: JSON.stringify(v) })
              message.success('已创建')
              setOpen(false)
              await load()
            } catch (e) {
              message.error(e instanceof Error ? e.message : '创建失败')
            }
          }}
        >
          <Form.Item name="name" label="名称" rules={[{ required: true, message: '请输入名称' }]}>
            <Input />
          </Form.Item>
          <Form.Item name="bind" label="绑定地址" rules={[{ required: true }]}>
            <Input />
          </Form.Item>
          <Form.Item name="port" label="端口" rules={[{ required: true }]}>
            <InputNumber min={1} max={65535} style={{ width: '100%' }} />
          </Form.Item>
          <Button type="primary" htmlType="submit" block>
            创建
          </Button>
        </Form>
      </Drawer>
    </div>
  )
}
