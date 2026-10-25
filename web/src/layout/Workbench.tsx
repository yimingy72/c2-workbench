import type { ReactNode } from 'react'
import styles from './Workbench.module.css'

export function Workbench({
  left,
  main,
  right,
}: {
  left: ReactNode
  main: ReactNode
  right?: ReactNode
}) {
  return (
    <div className={right ? styles.root : `${styles.root} ${styles.two}`}>
      {left}
      {main}
      {right}
    </div>
  )
}

export function Pane({
  title,
  extra,
  fill,
  children,
}: {
  title: string
  extra?: ReactNode
  fill?: boolean
  children: ReactNode
}) {
  return (
    <section className={styles.pane}>
      <header className={styles.head}>
        <h2>{title}</h2>
        {extra}
      </header>
      <div className={fill ? styles.bodyFill : styles.body}>{children}</div>
    </section>
  )
}
