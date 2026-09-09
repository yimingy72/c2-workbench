import { Typography } from 'antd'

export default function PayloadHelp() {
  return (
    <div>
      <Typography.Title level={4}>上线说明</Typography.Title>
      <Typography.Paragraph>
        第一期不在控制台生成二进制。在授权环境交叉编译 implant 后拷到目标 Windows 主机运行。
      </Typography.Paragraph>
      <Typography.Paragraph>
        默认回连 <Typography.Text code>10.211.55.2:34567</Typography.Text>
        。修改 <Typography.Text code>implant/implant.c</Typography.Text> 里的 XOR 后 host/port 数组后重新编译。
      </Typography.Paragraph>
      <Typography.Paragraph>
        <pre>{`cd implant
make
# 得到 implant.exe （PE32+）`}</pre>
      </Typography.Paragraph>
      <Typography.Paragraph>
        先在「监听管理」创建与 implant 端口一致的 TCP 监听，再运行 exe。会话出现在「主机管理」。
      </Typography.Paragraph>
    </div>
  )
}
