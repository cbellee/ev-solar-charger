import { type ReactNode } from "react";
import clsx from "clsx";

interface CardProps {
  title?: ReactNode;
  action?: ReactNode;
  className?: string;
  children: ReactNode;
}

export function Card({ title, action, className, children }: CardProps) {
  return (
    <section
      className={clsx(
        "rounded-lg border border-gray-800 bg-gray-800/50 p-4 shadow-sm",
        className,
      )}
    >
      {(title || action) && (
        <header className="mb-3 flex items-center justify-between gap-2">
          {title && <h2 className="text-sm font-semibold text-gray-200">{title}</h2>}
          {action}
        </header>
      )}
      {children}
    </section>
  );
}
