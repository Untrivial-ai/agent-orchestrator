"use client";

import Image from "next/image";
import { useState } from "react";
import { TESTIMONIALS, type Testimonial } from "./constants";

function getInitials(name: string) {
	return name
		.split(" ")
		.map((n) => n[0])
		.join("")
		.toUpperCase()
		.slice(0, 2);
}

function Avatar({ src, name }: { src?: string; name: string }) {
	if (src) {
		return (
			<Image
				src={src}
				alt={name}
				width={40}
				height={40}
				className="size-10 rounded-full object-cover"
			/>
		);
	}

	return (
		<div className="size-10 rounded-full bg-muted flex items-center justify-center text-sm font-medium text-muted-foreground">
			{getInitials(name)}
		</div>
	);
}

function TestimonialCard({
	testimonial,
	className = "",
}: {
	testimonial: Testimonial;
	className?: string;
}) {
	const [showOriginal, setShowOriginal] = useState(false);
	const hasTranslation = !!testimonial.originalContent;
	const isExternal = testimonial.url.startsWith("http");

	return (
		<a
			href={testimonial.url}
			target={isExternal ? "_blank" : undefined}
			rel={isExternal ? "noopener noreferrer" : undefined}
			className={`mb-px w-full break-inside-avoid bg-card p-5 align-top hover:bg-muted/60 transition-colors md:mb-0 md:block md:min-h-[220px] ${className}`}
		>
			<div className="flex items-start gap-3">
				<Avatar src={testimonial.avatar} name={testimonial.author} />
				<div className="flex-1 min-w-0">
					<div className="flex items-center gap-2">
						<span className="font-semibold text-foreground text-sm">
							{testimonial.author}
						</span>
					</div>
					<span className="text-muted-foreground text-sm">
						{testimonial.role ?? testimonial.handle}
					</span>
				</div>
			</div>
			<p className="mt-3 text-foreground/90 text-[15px] leading-relaxed whitespace-pre-line">
				{showOriginal ? testimonial.originalContent : testimonial.content}
			</p>
			{hasTranslation && (
				<button
					type="button"
					onClick={(e) => {
						e.preventDefault();
						e.stopPropagation();
						setShowOriginal(!showOriginal);
					}}
					className="group mt-2 py-2 text-xs text-muted-foreground hover:text-foreground transition-colors"
				>
					<span className="group-hover:hidden">
						{showOriginal ? "Translated" : "Translated"}
					</span>
					<span className="hidden group-hover:inline">
						{showOriginal ? "Show translation" : "Show original"}
					</span>
				</button>
			)}
		</a>
	);
}

export function WallOfLoveSection() {
	const testimonials = TESTIMONIALS.slice(0, 6);

	return (
		<section className="relative px-4 py-16 sm:px-8 sm:py-20 lg:px-[30px] lg:py-24">
			<div className="max-w-7xl mx-auto">
				<div className="text-left mb-12 select-none">
					<h2 className="text-2xl sm:text-3xl lg:text-4xl font-semibold text-foreground">
						In the wild
					</h2>
					<p className="mt-3 text-base text-muted-foreground">
						Real feedback from builders using AO.
					</p>
				</div>

				<div className="columns-1 gap-px bg-border md:grid md:grid-cols-3">
					{testimonials.map((testimonial, index) => (
						<TestimonialCard
							key={testimonial.id}
							testimonial={testimonial}
							className={index >= 4 ? "hidden md:block" : "inline-block"}
						/>
					))}
				</div>
			</div>
		</section>
	);
}
