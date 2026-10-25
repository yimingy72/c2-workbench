import { useCallback, useEffect, useState } from 'react'
import { Button, Form, Input, InputNumber, Modal, Popconfirm, Table, Tag, App } from 'antd'
import { PlusOutlined } from '@ant-design/icons'
import { api } from '../api'
import Page from '../layout/Page'
import styles from './console.module.css'

type Listener = { id: string; name: string; bind: string; port: number }

export default function Listeners() {
  const { message } = App.useApp()
  const [rows, setRows] = useState<Listener[]>([])
  const [open, setOpen] = useState(false)
  const [pending, setPending] = useState(false)
  const [form] = Form.useForm()

  const load = useCallback(async () => {
    try {
      const data = (await api('/api/listeners')) as Listener[]
      setRows(Array.isArray(data) ? data : [])
    } catch (e) {
      message.error(e instanceof Error ? e.message : '加载失败')
    }
  }, [message])

  useEffect(() => {
    void load()
  }, [load])

  return (
    <Page
      title="监听管理"
      extra={
        <>
          <Button onClick={() => void load()}>刷新</Button>
          <Button type="primary" icon={<PlusOutlined />} onClick={() => setOpen(true)}>
            新增监听
          </Button>
        </>
      }
    >
      <div className={`${styles.card} ${styles.tableCard}`}>
        <Table
          rowKey="id"
          size="middle"
          pagination={false}
          dataSource={rows}
          locale={{ emptyText: '还没有监听器。新建一个 TCP 端口后，载荷才能回连上线。' }}
          columns={[
            { title: '名称', dataIndex: 'name' },
            {
              title: '协议',
              width: 110,
              render: () => <Tag color="blue">xor-tcp</Tag>,
            },
            {
              title: '绑定',
              render: (_: unknown, r: Listener) => (
                <span className={styles.mono}>
                  {r.bind}:{r.port}
                </span>
              ),
            },
            {
              title: '状态',
              width: 100,
              render: () => <Tag color="success">运行中</Tag>,
            },
            { title: '编号', dataIndex: 'id', width: 120, render: (v: string) => <span className={styles.mono}>{v}</span> },
            {
              title: '操作',
              width: 100,
              render: (_: unknown, r: Listener) => (
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
                  <Button type="link" danger size="small">
                    删除
                  </Button>
                </Popconfirm>
              ),
            },
          ]}
        />
      </div>
      <Modal
        title="新增监听"
        open={open}
        onCancel={() => {
          setOpen(false)
          form.resetFields()
        }}
        footer={null}
        destroyOnHidden
      >
        <Form
          form={form}
          layout="vertical"
          initialValues={{ bind: '0.0.0.0', port: 34567, name: 'tcp-34567' }}
          onFinish={async (v: { name: string; bind: string; port: number }) => {
            setPending(true)
            try {
              await api('/api/listeners', { method: 'POST', body: JSON.stringify(v) })
              message.success('已创建')
              setOpen(false)
              form.resetFields()
              await load()
            } catch (e) {
              message.error(e instanceof Error ? e.message : '创建失败')
            } finally {
              setPending(false)
            }
          }}
        >
          <Form.Item name="name" label="名称">
            <Input placeholder="留空则使用 tcp-端口" />
          </Form.Item>
          <Form.Item name="bind" label="绑定地址" rules={[{ required: true }]}>
            <Input />
          </Form.Item>
          <Form.Item name="port" label="端口" rules={[{ required: true }]}>
            <InputNumber
              min={1}
              max={65535}
              style={{ width: '100%' }}
              onChange={(port) => {
                const name = form.getFieldValue('name')
                if (!name || /^tcp-\d+$/.test(name)) form.setFieldsValue({ name: `tcp-${port || ''}` })
              }}
            />
          </Form.Item>
          <Button type="primary" htmlType="submit" loading={pending} block>
            创建
          </Button>
        </Form>
      </Modal>
    </Page>
  )
}
