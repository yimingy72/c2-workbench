import type { ReactNode } from 'react'
import styles from './Page.module.css'

export default function Page({
  title,
  extra,
  children,
}: {
  title: string
  extra?: ReactNode
  children: ReactNode
}) {
  return (
    <div className={styles.page}>
      <header className={styles.head}>
        <h2 className={styles.title}>{title}</h2>
        {extra ? <div className={styles.extra}>{extra}</div> : null}
      </header>
      <div className={styles.body}>{children}</div>
    </div>
  )
}
